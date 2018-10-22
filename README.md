# cfexecute
[![Build Status](https://travis-ci.org/cep21/cfexecute.svg?branch=master)](https://travis-ci.org/cep21/cfexecute)

It is `aws cloudformation create-change-set`, but made for the console.

# How to install

`go get github.com/cep21/cfexecute`

# What it does

This program follows these general steps:

1) Parse changeset parameters file as a golang text template, exposing Env variables to the template.
2) Create the changeset.
3) Show the changeset to stdout.
4) Optionally confirm if you want to apply the changset.
5) Stream cloudwatch events from applying the changeset to stdout.
6) Optionally rollback the changeset if things, like ECS services, fail to stablize (rather than wait 3 hours).
7) Watch for SIGINT and rollback the changeset early if needed

# How to use

Create a file [changeset.json](changeset.json) which should follow the template of `aws cloudformation create-change-set --generate-cli-skeleton`
as documented on [AWS](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_CreateChangeSet.html).  This
changeset is parsed as a Go template, with helper functions defined on the Ctx object.  Once created, the changeset
can be executed and output from the execution should be streamed to STDOUT.

# Example output

```bash
< go run *.go -clean
2018/10/19 11:36:09 executing as arn:aws:sts::123123123123:assumed-role/me/me
2018/10/19 11:36:09 guessed changeset type UPDATE
2018/10/19 11:36:09 using client token cfexecute-1539974169635588000.  Creating changeset
2018/10/19 11:36:09 Changeset created: arn:aws:cloudformation:us-west-2:123123123123:changeSet/cfexecute-test/dfd45cd0-89d6-4ca5-9bf8-b8a78a985a17
2018/10/19 11:36:09 Finished creating changeset
2018/10/19 11:36:10 ChangeSet status set to CREATE_IN_PROGRESS: 
2018/10/19 11:36:12 ChangeSet status set to CREATE_COMPLETE: 
2018/10/19 11:36:12 Changeset state finalized
2018/10/19 11:36:12 Printing changeset changes
{
	"Capabilities": null,
	"ChangeSetId": "arn:aws:cloudformation:us-west-2:123123123123:changeSet/cfexecute-test/dfd45cd0-89d6-4ca5-9bf8-b8a78a985a17",
	"ChangeSetName": "cfexecute-test",
	"Changes": [
		{
			"ResourceChange": {
				"Action": "Modify",
				"Details": [
					{
						"CausingEntity": null,
						"ChangeSource": "DirectModification",
						"Evaluation": "Static",
						"Target": {
							"Attribute": "Properties",
							"Name": "ClusterName",
							"RequiresRecreation": "Always"
						}
					}
				],
				"LogicalResourceId": "ExampleCluster",
				"PhysicalResourceId": "Bob3",
				"Replacement": "True",
				"ResourceType": "AWS::ECS::Cluster",
				"Scope": [
					"Properties"
				]
			},
			"Type": "Resource"
		}
	],
	"CreationTime": "2018-10-19T18:36:08.903Z",
	"Description": "This is a description again",
	"ExecutionStatus": "AVAILABLE",
	"NextToken": null,
	"NotificationARNs": null,
	"Parameters": null,
	"RollbackConfiguration": {
		"MonitoringTimeInMinutes": 0,
		"RollbackTriggers": null
	},
	"StackId": "arn:aws:cloudformation:us-west-2:123123123123:stack/hi/0c405700-d2ef-11e8-8f9b-503ac9ec24d1",
	"StackName": "hi",
	"Status": "CREATE_COMPLETE",
	"StatusReason": null,
	"Tags": null
}
Should we apply these changes [y/n]: y
2018/10/19 11:36:13 Executing changeset
2018/10/19 11:36:13 Following along on changeset execution
2018/10/19 11:36:14 Stack event: {
  LogicalResourceId: "hi",
  PhysicalResourceId: "arn:aws:cloudformation:us-west-2:123123123123:stack/hi/0c405700-d2ef-11e8-8f9b-503ac9ec24d1",
  ResourceStatus: "UPDATE_IN_PROGRESS",
  ResourceStatusReason: "User Initiated",
  ResourceType: "AWS::CloudFormation::Stack"
}
2018/10/19 11:36:14 Stack status set to UPDATE_IN_PROGRESS: 
2018/10/19 11:36:19 Stack event: {
  LogicalResourceId: "ExampleCluster",
  PhysicalResourceId: "Bob3",
  ResourceStatus: "UPDATE_IN_PROGRESS",
  ResourceStatusReason: "Requested update requires the creation of a new physical resource; hence creating one.",
  ResourceType: "AWS::ECS::Cluster"
}
2018/10/19 11:36:20 Stack event: {
  LogicalResourceId: "ExampleCluster",
  PhysicalResourceId: "Bob2",
  ResourceStatus: "UPDATE_IN_PROGRESS",
  ResourceStatusReason: "Resource creation Initiated",
  ResourceType: "AWS::ECS::Cluster"
}
2018/10/19 11:36:20 Stack event: {
  LogicalResourceId: "ExampleCluster",
  PhysicalResourceId: "Bob2",
  ResourceStatus: "UPDATE_COMPLETE",
  ResourceStatusReason: "",
  ResourceType: "AWS::ECS::Cluster"
}
2018/10/19 11:36:21 Stack status set to UPDATE_COMPLETE_CLEANUP_IN_PROGRESS: 
2018/10/19 11:36:21 Stack event: {
  LogicalResourceId: "hi",
  PhysicalResourceId: "arn:aws:cloudformation:us-west-2:123123123123:stack/hi/0c405700-d2ef-11e8-8f9b-503ac9ec24d1",
  ResourceStatus: "UPDATE_COMPLETE_CLEANUP_IN_PROGRESS",
  ResourceStatusReason: "",
  ResourceType: "AWS::CloudFormation::Stack"
}
2018/10/19 11:36:22 Stack event: {
  LogicalResourceId: "ExampleCluster",
  PhysicalResourceId: "Bob3",
  ResourceStatus: "DELETE_IN_PROGRESS",
  ResourceStatusReason: "",
  ResourceType: "AWS::ECS::Cluster"
}
2018/10/19 11:36:23 Stack status set to UPDATE_COMPLETE: 

```