package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"text/template"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awsutil"
	"github.com/aws/aws-sdk-go/aws/client"
	"github.com/aws/aws-sdk-go/aws/credentials/stscreds"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/cloudformation"
	"github.com/aws/aws-sdk-go/service/sts"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

// Application contains the logic of our appplication
type Application struct {
	flagset                *flag.FlagSet
	changesetFilename      string
	args                   []string
	ChangeSetExecutor      ChangeSetExecutor
	Ctx                    Ctx
	awsConfig              *aws.Config
	osExit                 func(int)
	timeout                time.Duration
	logVerbosity           int
	autoConfirm            bool
	cleanExistingChangeset bool

	in  io.Reader
	out io.Writer
	err io.Writer
}

// Logger helps us verbose log output
type Logger struct {
	logVerbosity int
	log          *log.Logger
	mu           sync.Mutex
}

// Log will log.Printf the args if verbosity <= this logger's verbosity
func (l *Logger) Log(verbosity int, msg string, args ...interface{}) {
	if verbosity > l.logVerbosity {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	log.Printf(msg, args...)
}

var app = Application{
	args:   os.Args[1:],
	in:     os.Stdin,
	out:    os.Stdout,
	osExit: os.Exit,
	//awsConfig: defaults.Get().Config,
	err:     os.Stderr,
	flagset: flag.NewFlagSet(os.Args[0], flag.ExitOnError),
}

// Main executes our application
func (a *Application) Main() {
	err := a.mainReturnCode()
	if err != nil {
		if _, fmtErr := fmt.Fprintf(a.err, "EXECUTION ERROR: %s\n", err.Error()); fmtErr != nil {
			panic(fmtErr)
		}
		a.osExit(1)
		return
	}
	a.osExit(0)
}

func (a *Application) debugLogUser(ctx context.Context, awsSession client.ConfigProvider, logger *Logger) {
	if logger.logVerbosity == 0 {
		return
	}
	stsClient := sts.New(awsSession)
	identOut, err := stsClient.GetCallerIdentityWithContext(ctx, nil)
	if err != nil {
		logger.Log(1, "unable to verify caller identity: %s", err.Error())
		return
	}
	logger.Log(1, "executing as %s", emptyOnNil(identOut.Arn))
}

func emptyOnNil(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// mainReturnCode contains the application execution logic, but does not call os.Exit so we can
// still execute any defer function calls
func (a *Application) mainReturnCode() error {
	ctx := context.Background()
	if err := a.parseFlags(); err != nil {
		return errors.Wrap(err, "unable to aprse flags")
	}
	if a.timeout > 0 {
		var cancel func()
		ctx, cancel = context.WithTimeout(ctx, a.timeout)
		defer cancel()
	}
	logger := &Logger{
		log:          log.New(a.err, "ecsrun", log.LstdFlags),
		logVerbosity: a.logVerbosity,
	}
	awsSession, err := a.setupAWS()
	if err != nil {
		return errors.Wrap(err, "unable to make aws session")
	}
	a.debugLogUser(ctx, awsSession, logger)
	creationInput, err := loadCreateChangeSet(a.changesetFilename, &CreateChangeSetTemplate{
		Ctx: a.Ctx,
	}, logger)
	if err != nil {
		return errors.Wrap(err, "unable to load and parse changeset creation template")
	}
	logger.Log(2, "Changeset Created: %s", awsutil.Prettify(creationInput))
	cloudformationClient := cloudformation.New(awsSession)
	return a.ChangeSetExecutor.Run(ctx, cloudformationClient, creationInput, a.out, a.in, logger, a.autoConfirm, a.cleanExistingChangeset)
}

func (a *Application) setupAWS() (*session.Session, error) {
	defaultSession, err := session.NewSession(a.awsConfig)
	if err != nil {
		return nil, err
	}
	assumedRole := os.Getenv("ASSUME_ROLE")
	if assumedRole == "" {
		return defaultSession, nil
	}
	var newCfg aws.Config
	if a.awsConfig != nil {
		newCfg = *a.awsConfig
	}
	newCfg.MergeIn(&aws.Config{
		Credentials: stscreds.NewCredentials(defaultSession, assumedRole),
	})
	return session.NewSession(&newCfg)
}

func loadCreateChangeSet(changesetFilename string, translator *CreateChangeSetTemplate, logger *Logger) (*cloudformation.CreateChangeSetInput, error) {
	f, err := os.Open(changesetFilename)
	if err != nil {
		return nil, errors.Wrapf(err, "unable to open file %s (does it exist?)", changesetFilename)
	}
	return translator.createRegisterTaskDefinitionInput(f, logger)
}

func (a *Application) parseFlags() error {
	a.flagset.StringVar(&a.changesetFilename, "changeset", "changeset.json", "Filename of the create changeset call")
	a.flagset.DurationVar(&a.timeout, "timeout", 0, "A maximum duration the command will run")
	a.flagset.IntVar(&a.logVerbosity, "verbosity", 1, "Higher values output more program context.  Zero just outputs the task's stderr/stdout")
	a.flagset.BoolVar(&a.autoConfirm, "auto", false, "If true, user will not be prompted to confirm cloudformation changes")
	a.flagset.BoolVar(&a.cleanExistingChangeset, "clean", false, "Will remove any existing changesets with this changeset's name")
	return a.flagset.Parse(a.args)
}

// CreateChangeSetTemplate is passed to the changeset.json file when Executing the template
type CreateChangeSetTemplate struct {
	Ctx
}

func (t *CreateChangeSetTemplate) createRegisterTaskDefinitionInput(in io.Reader, logger *Logger) (*cloudformation.CreateChangeSetInput, error) {
	readerContents, err := ioutil.ReadAll(in)
	if err != nil {
		return nil, errors.Wrap(err, "unable to fully read from reader (verify your reader)")
	}
	taskTemplate, err := template.New("task_template").Parse(string(readerContents))
	if err != nil {
		return nil, errors.Wrap(err, "invalid task template (make sure your task template is ok)")
	}
	logger.Log(2, "Task template: %s", string(readerContents))
	var templateResult bytes.Buffer
	if err := taskTemplate.Execute(&templateResult, t); err != nil {
		return nil, errors.Wrap(err, "unable to execute task template (are you calling invalid functions?)")
	}
	logger.Log(2, "Executed template result: %s", templateResult.String())
	var out cloudformation.CreateChangeSetInput
	if err := json.NewDecoder(&templateResult).Decode(&out); err != nil {
		templateResult.Reset()
		logger.Log(1, "Failing template body: %s", templateResult.String())
		return nil, errors.Wrap(err, "unable to deserialize given template (is it valid json?)")
	}
	return &out, nil
}

// ChangeSetExecutor controls the logic of executing and streaming a changeset creation
type ChangeSetExecutor struct {
	PollInterval time.Duration
}

func (t *ChangeSetExecutor) pollInterval() time.Duration {
	if t.PollInterval == 0 {
		return time.Second
	}
	return t.PollInterval
}

func (t *ChangeSetExecutor) guessChangesetType(ctx context.Context, cloudformationClient *cloudformation.CloudFormation, in *cloudformation.CreateChangeSetInput, logger *Logger) (*cloudformation.CreateChangeSetInput, error) {
	if in == nil || in.ChangeSetType == nil {
		return in, nil
	}
	if *in.ChangeSetType != "GUESS" {
		return in, nil
	}
	_, err := cloudformationClient.DescribeStacksWithContext(ctx, &cloudformation.DescribeStacksInput{
		StackName: in.StackName,
	})
	if err != nil {
		// stack does not exist (probably)
		in.ChangeSetType = aws.String("CREATE")
	} else {
		in.ChangeSetType = aws.String("UPDATE")
	}
	logger.Log(1, "guessed changeset type %s", *in.ChangeSetType)
	return in, nil
}

func (t *ChangeSetExecutor) waitForChangesetToFinishCreating(ctx context.Context, cloudformationClient *cloudformation.CloudFormation, changesetARN string, logger *Logger, cleanShutdown <-chan struct{}) (*cloudformation.DescribeChangeSetOutput, error) {
	lastChangesetStatus := ""
	for {
		select {
		case <-time.After(t.pollInterval()):
		case <-ctx.Done():
		case <-cleanShutdown:
			return nil, nil
		}
		out, err := cloudformationClient.DescribeChangeSetWithContext(ctx, &cloudformation.DescribeChangeSetInput{
			ChangeSetName: &changesetARN,
		})
		if err != nil {
			return nil, errors.Wrap(err, "unable to describe changeset")
		}
		stat := emptyOnNil(out.Status)
		if stat != lastChangesetStatus {
			logger.Log(1, "ChangeSet status set to %s: %s", stat, emptyOnNil(out.StatusReason))
			lastChangesetStatus = stat
		}
		// All terminal states
		if stat == "CREATE_COMPLETE" || stat == "FAILED" || stat == "DELETE_COMPLETE" {
			return out, nil
		}
	}
}

func (t *ChangeSetExecutor) printOutChanges(desc *cloudformation.DescribeChangeSetOutput, runOutput io.Writer) error {
	e := json.NewEncoder(runOutput)
	e.SetIndent("", "\t")
	e.SetEscapeHTML(false)
	return e.Encode(desc)
}

// waitForTerminalState loops forever until either the context ends, or something fails
func (t *ChangeSetExecutor) waitForTerminalState(ctx context.Context, cloudformationClient *cloudformation.CloudFormation, stackID string, logger *Logger) error {
	lastStackStatus := ""
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(t.pollInterval()):
		}
		descOut, err := cloudformationClient.DescribeStacksWithContext(ctx, &cloudformation.DescribeStacksInput{
			StackName: &stackID,
		})
		if err != nil {
			return err
		}
		if len(descOut.Stacks) != 1 {
			return errors.Errorf("unable to correctly find stack %s", stackID)
		}
		thisStack := descOut.Stacks[0]
		if *thisStack.StackStatus != lastStackStatus {
			logger.Log(1, "Stack status set to %s: %s", *thisStack.StackStatus, emptyOnNil(thisStack.StackStatusReason))
			lastStackStatus = *thisStack.StackStatus
		}
		// https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/using-cfn-describing-stacks.html
		terminalFailureStatusStates := map[string]struct{}{
			"CREATE_FAILED":            {},
			"DELETE_FAILED":            {},
			"ROLLBACK_FAILED":          {},
			"ROLLBACK_COMPLETE":        {},
			"UPDATE_ROLLBACK_COMPLETE": {},
			"UPDATE_ROLLBACK_FAILED":   {},
		}
		if _, exists := terminalFailureStatusStates[emptyOnNil(thisStack.StackStatus)]; exists {
			return errors.Errorf("Terminal stack state failure: %s %s", emptyOnNil(thisStack.StackStatus), emptyOnNil(thisStack.StackStatusReason))
		}
		terminalOkStatusStates := map[string]struct{}{
			"CREATE_COMPLETE": {},
			"DELETE_COMPLETE": {},
			"UPDATE_COMPLETE": {},
		}
		if _, exists := terminalOkStatusStates[emptyOnNil(thisStack.StackStatus)]; exists {
			return nil
		}
	}
}

func genClientToken() string {
	return fmt.Sprintf("cfexecute-%d", time.Now().UnixNano())
}

type terminateEarly func(context.Context)
type executeTillSignal func(context.Context, <-chan struct{}) error

// cleanupOnSignalOrError takes an object and uses that object.  If during use, SIGINT or SIGTERM is seen,
// the object is cleaned up early.  If useX returns an error, the object is also terminated.  The object should not
// terminate more than once
func cleanupOnSignalOrError(ctx context.Context, signals []os.Signal, terminateX terminateEarly, useX executeTillSignal) (retErr error) {
	useResult := make(chan error, 1)
	noLongerWaitingOnSignal := make(chan struct{}, 3)
	eg, egCtx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		sigChan := make(chan os.Signal, len(signals))
		signal.Notify(sigChan, signals...)
		//defer close(noLongerWaitingOnSignal)
		defer close(sigChan)
		defer signal.Stop(sigChan)
		select {
		case <-egCtx.Done():
			return egCtx.Err()
		case caughtSig := <-sigChan:
			terminateX(ctx)
			return errors.Errorf("terminate called since SIG caught: %s", caughtSig.String())
		case useErr := <-useResult:
			if useErr != nil {
				terminateX(ctx)
			}
			return nil
		}
	})
	eg.Go(func() error {
		// The chan noLongerWaitingOnSignal is closed to tell useX that it should try to exit early since
		// a signal was seen and terminateX was called
		// Note: Do not pass in "egCtx" since we actually want `useX` to possibly continue even after we terminate
		ret := useX(ctx, noLongerWaitingOnSignal)
		useResult <- ret
		return ret
	})
	return eg.Wait()
}

func (t *ChangeSetExecutor) useCreatedChangeSet(cloudformationClient *cloudformation.CloudFormation, changesetARN string, logger *Logger, runOutput io.Writer, autoConfirm bool, userInput io.Reader, clientToken string) executeTillSignal {
	return func(ctx context.Context, cleanShutdown <-chan struct{}) error {
		logger.Log(1, "Finished creating changeset")
		state, err := t.waitForChangesetToFinishCreating(ctx, cloudformationClient, changesetARN, logger, cleanShutdown)
		if err != nil {
			return errors.Wrap(err, "unable to wait for changeset to stabilize")
		}
		logger.Log(1, "Changeset state finalized")
		if emptyOnNil(state.Status) == "FAILED" {
			return errors.Errorf("unable to create changeset: %s", *state.StatusReason)
		}
		logger.Log(1, "Printing changeset changes")
		if err := t.printOutChanges(state, runOutput); err != nil {
			return errors.Wrap(err, "unable to print out changeset description")
		}
		if !autoConfirm {
			if !confirm(userInput, runOutput, "Should we apply these changes", 3, cleanShutdown) {
				return errors.New("user aborted changes")
			}
		}
		logger.Log(1, "Executing changeset")
		if _, executeChangesetErr := cloudformationClient.ExecuteChangeSetWithContext(ctx, &cloudformation.ExecuteChangeSetInput{
			ChangeSetName:      &changesetARN,
			ClientRequestToken: &clientToken,
		}); executeChangesetErr != nil {
			return errors.Wrapf(executeChangesetErr, "unable to execute changeset %s", changesetARN)
		}
		return nil
	}
}

func (t *ChangeSetExecutor) removeChangeset(cloudformationClient *cloudformation.CloudFormation, changesetArn string, logger *Logger) terminateEarly {
	return func(ctx context.Context) {
		logger.Log(1, "Removing changeset")
		_, err := cloudformationClient.DeleteChangeSetWithContext(ctx, &cloudformation.DeleteChangeSetInput{
			ChangeSetName: &changesetArn,
		})
		if err != nil {
			logger.Log(1, "unable to clean up changeset: %s", err.Error())
		}
	}
}

// Run executes the changeset creation logic
func (t *ChangeSetExecutor) Run(ctx context.Context, cloudformationClient *cloudformation.CloudFormation, in *cloudformation.CreateChangeSetInput, runOutput io.Writer, userInput io.Reader, logger *Logger, autoConfirm bool, cleanExistingCS bool) error {
	catchSignals := []os.Signal{
		os.Interrupt, syscall.SIGTERM,
	}
	in, guessErr := t.guessChangesetType(ctx, cloudformationClient, in, logger)
	if guessErr != nil {
		return errors.New("unable to guess changeset type for stack")
	}
	clientToken := genClientToken()
	logger.Log(1, "using client token %s.  Creating changeset", clientToken)
	in.ClientToken = &clientToken
	if cleanExistingCS {
		_, err := cloudformationClient.DeleteChangeSetWithContext(ctx, &cloudformation.DeleteChangeSetInput{
			StackName:     in.StackName,
			ChangeSetName: in.ChangeSetName,
		})
		if err != nil {
			logger.Log(3, "unable to delete existing changeset.  Maybe it never existed.  You can probably ignore this: %s", err.Error())
		}
	}

	changesetToExecute, err := cloudformationClient.CreateChangeSetWithContext(ctx, in)
	if err != nil {
		logger.Log(1, "Changeset Template: \n%s", emptyOnNil(in.TemplateBody))
		in.TemplateBody = nil
		logger.Log(1, "Changeset: %s", awsutil.Prettify(in))
		return errors.Wrap(err, "unable to create changeset for stack")
	}
	logger.Log(1, "Changeset created: %s", *changesetToExecute.Id)

	stackID := *changesetToExecute.StackId
	changesetARN := *changesetToExecute.Id
	if err := cleanupOnSignalOrError(ctx, catchSignals, t.removeChangeset(cloudformationClient, changesetARN, logger), t.useCreatedChangeSet(cloudformationClient, changesetARN, logger, runOutput, autoConfirm, userInput, clientToken)); err != nil {
		if strings.Contains(err.Error(), "didn't contain changes") {
			logger.Log(1, "Changeset contains no changes!")
			return nil
		}
		return errors.Wrap(err, "unable to use created changeset")
	}
	logger.Log(1, "Changeset executed: waiting for stack to stabilize")

	eg, egCtx := errgroup.WithContext(ctx)
	ss := StackStreamer{
		PollInterval: t.PollInterval,
	}
	eg.Go(func() error {
		defer func() {
			if err := ss.Close(); err != nil {
				logger.Log(1, "uanble to close stack streamer: %s", err.Error())
			}
		}()
		return cleanupOnSignalOrError(egCtx, catchSignals,
			t.cancelStackUpdates(cloudformationClient, logger, clientToken, stackID),
			t.waitForStackToStablize(cloudformationClient, stackID, logger))
	})
	eg.Go(func() error {
		return ss.Start(ctx, cloudformationClient, logger, stackID, clientToken)
	})
	logger.Log(1, "Following along on changeset execution")
	return eg.Wait()
}

func (t *ChangeSetExecutor) waitForStackToStablize(cloudformationClient *cloudformation.CloudFormation, stackID string, logger *Logger) executeTillSignal {
	return func(ctx context.Context, cleanStop <-chan struct{}) error {
		return t.waitForTerminalState(ctx, cloudformationClient, stackID, logger)
	}
}

func (t *ChangeSetExecutor) cancelStackUpdates(cloudformationClient *cloudformation.CloudFormation, logger *Logger, clientToken string, stackID string) terminateEarly {
	return func(ctx context.Context) {
		logger.Log(1, "canceling stack update")
		_, err := cloudformationClient.CancelUpdateStackWithContext(ctx, &cloudformation.CancelUpdateStackInput{
			ClientRequestToken: aws.String(clientToken + "-canceled"),
			StackName:          &stackID,
		})
		if err != nil {
			logger.Log(1, "error canceling stack update: %s", err.Error())
		}
	}
}

// confirm displays a prompt `s` to the user and returns a bool indicating yes / no
// If the lowercased, trimmed input begins with anything other than 'y', it returns false
// It accepts an int `tries` representing the number of attempts before returning false
func confirm(in io.Reader, out io.Writer, prompt string, tries int, _ <-chan struct{}) bool { //nolint: unparam
	r := bufio.NewReader(in)

	for ; tries > 0; tries-- {
		if _, err := fmt.Fprintf(out, "%s [y/n]: ", prompt); err != nil {
			return false
		}

		res, err := r.ReadString('\n')
		if err != nil {
			return false
		}

		// Empty input (i.e. "\n")
		if len(res) < 2 {
			continue
		}

		return strings.ToLower(strings.TrimSpace(res))[0] == 'y'
	}

	return false
}

// Ctx contains fun helper functions that make template generation easier
type Ctx struct{}

// Env calls out to os.Getenv
func (t Ctx) Env(key string) string {
	return os.Getenv(key)
}

// File loads a filename into the template
func (t Ctx) File(key string) (string, error) {
	b, err := ioutil.ReadFile(key)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// JSON converts a string into a JSON string
func (t Ctx) JSON(key string) (string, error) {
	res, err := json.Marshal(key)
	if err != nil {
		return "", err
	}
	return string(res), nil
}

// JSONStr converts a string into a JSON string, but does not return the starting and ending "
// This lets you use a JSON template that is itself still JSON
func (t Ctx) JSONStr(key string) (string, error) {
	res, err := json.Marshal(key)
	if err != nil {
		return "", err
	}
	if len(res) < 2 {
		return "", errors.Errorf("Invalid json str %s", res)
	}
	if res[0] != '"' || res[len(res)-1] != '"' {
		return "", errors.Errorf("Invalid json str quotes %s", res)
	}
	return string(res[1 : len(res)-1]), nil
}

// MustEnv is like Env, but will error if the env variable is empty
func (t Ctx) MustEnv(key string) (string, error) {
	if ret := t.Env(key); ret != "" {
		return ret, nil
	}
	return "", errors.Errorf("Unable to find environment variable %s", key)
}

func main() {
	app.Main()
}
