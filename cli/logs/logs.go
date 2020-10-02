package logs

import (
	"bufio"
	"context"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/buger/goterm"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/kelda/blimp/cli/config"
	"github.com/kelda/blimp/cli/manager"
	"github.com/kelda/blimp/pkg/errors"
	"github.com/kelda/blimp/pkg/names"
)

type Command struct {
	Services []string
	Opts     corev1.PodLogOptions
	Config   config.Config

	svcStatus map[string]*statusNotifier
}

type rawLogLine struct {
	// Any error that occurred when trying to read logs.
	// If this is non-nil, `message` and `receivedAt` aren't meaningful.
	error error

	// The container that generated the log.
	fromContainer string

	// The contents of the log line (including the timestamp added by Kubernetes).
	message string

	// The time that we read the log line.
	receivedAt time.Time
}

type parsedLogLine struct {
	// The Kelda container that generated the log.
	fromContainer string

	// The contents of the log line (without the timestamp added by Kubernetes).
	message string

	// The time that the log line was generated by the application according to
	// the machine that the container is running on.
	loggedAt time.Time

	// Specifies the exact string that should be printed for this log line. If
	// this is present, fromContainer and message are both ignored while
	// printing the log.
	formatOverride string
}

func New() *cobra.Command {
	cmd := &Command{}

	cobraCmd := &cobra.Command{
		Use:   "logs SERVICE ...",
		Short: "Print the logs for the given services",
		Long: "Print the logs for the given services.\n\n" +
			"If multiple services are provided, the log output is interleaved.",
		Run: func(_ *cobra.Command, args []string) {
			blimpConfig, err := config.GetConfig()
			if err != nil {
				errors.HandleFatalError(err)
			}

			if len(args) == 0 {
				fmt.Fprintln(os.Stderr, "At least one container is required.")
				os.Exit(1)
			}

			cmd.Config = blimpConfig
			cmd.Services = args
			if err := cmd.Run(context.Background()); err != nil {
				errors.HandleFatalError(err)
			}
		},
	}

	cobraCmd.Flags().BoolVarP(&cmd.Opts.Follow, "follow", "f", false,
		"Specify if the logs should be streamed.")
	cobraCmd.Flags().BoolVarP(&cmd.Opts.Previous, "previous", "p", false,
		"If true, print the logs for the previous instance of the container if it crashed.")

	return cobraCmd
}

func (cmd Command) Run(ctx context.Context) error {
	kubeClient, _, err := cmd.Config.Auth.KubeClient()
	if err != nil {
		return errors.WithContext("connect to cluster", err)
	}

	for _, container := range cmd.Services {
		// For logs to work, the container needs to have started, but it doesn't
		// necessarily need to be running.
		err = manager.CheckServiceStarted(container, cmd.Config.BlimpAuth())
		if err != nil {
			return err
		}
	}

	// Exit gracefully when the user Ctrl-C's.
	// The `printLogs` function will return when the context is cancelled,
	// which allows functions defered in this method to run.
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		<-signalChan
		cancel()
	}()

	if cmd.Opts.Follow {
		if err := cmd.startStatusUpdater(ctx); err != nil {
			return errors.WithContext("start status updater", err)
		}
	}

	// runningCount should equal the number of containers we are currently
	// tailing.
	// Using a WaitGroup for this counter would be the obvious choice, but it is
	// only safe to Add to a WaitGroup when
	// - the count is >0, or
	// - there is no active Wait().
	// Since we want to be able to reconnect to logs and increment the counter
	// when this happens, we won't be in the second condition and we can't
	// guarantee the first. Even though it would be ok with us for the Add to
	// simply fail in this case, this can cause a panic, which is
	// unacceptable. So, we do not use a WaitGroup.
	runningCount := len(cmd.Services)
	// runningCountCond should be Signaled when the runningCount is decremented
	// to 0, so that we can Wait to watch for when it reaches 0.
	runningCountCond := sync.NewCond(&sync.Mutex{})
	combinedLogs := make(chan rawLogLine, len(cmd.Services)*32)
	for _, service := range cmd.Services {
		go func(service string) {
			for {
				err := cmd.forwardLogs(ctx, combinedLogs, service, kubeClient)
				if err != nil && errors.RootCause(err) != io.EOF && err != context.Canceled {
					log.WithError(err).Debug("Dirty logs termination")
				}

				// Indicate that we don't have more logs to send.
				runningCountCond.L.Lock()
				runningCount--
				if runningCount == 0 {
					runningCountCond.Signal()
				}
				runningCountCond.L.Unlock()

				if err == context.Canceled {
					return
				}

				// If we aren't following logs, we are done for good.
				if !cmd.Opts.Follow {
					return
				}

				// Otherwise, wait to see if the container restarts.
				select {
				case <-ctx.Done():
					return
				case <-cmd.svcStatus[service].Running():
					printStatusMessage(service, "The service has restarted, reconnecting...", len(cmd.Services) == 1)
				}

				// If the container has restarted, start tailing logs again.
				runningCountCond.L.Lock()
				runningCount++
				runningCountCond.L.Unlock()
			}
		}(service)
	}

	// If all the containers we were logging have exited, we are done and should
	// exit. Note: If you restart all your containers at the same time, we might
	// exit because this is indistinguishable from all the containers exiting
	// normally.
	go func() {
		runningCountCond.L.Lock()
		for runningCount > 0 {
			runningCountCond.Wait()
		}
		runningCountCond.L.Unlock()
		cancel()
	}()

	hideServiceName := len(cmd.Services) == 1
	return printLogs(ctx, combinedLogs, hideServiceName)
}

// forwardLogs forwards each log line from `logsReq` to the `combinedLogs`
// channel. If we are following logs, this function should only return if the
// container exits.
func (cmd *Command) forwardLogs(ctx context.Context, combinedLogs chan<- rawLogLine,
	service string, kubeClient kubernetes.Interface) error {
	var lastMessageTime, sinceTime time.Time

	isOldMessage := func(message string) bool {
		if message == "" {
			return false
		}

		_, timestamp, err := parseLogLine(message)
		if err != nil {
			return false
		}

		if !timestamp.After(sinceTime) {
			return true
		}

		if timestamp.After(lastMessageTime) {
			lastMessageTime = timestamp
		}
		return false
	}

	var podExited <-chan struct{}
	if cmd.Opts.Follow {
		// We call Exited() before doing anything to avoid a race where the pod
		// restarts before we finish processing logs, causing us to miss the exit.
		// This way, we use only a single channel for the whole function and will
		// exit once this channel is closed (that is, the pod exits).
		podExited = cmd.svcStatus[service].Exited()
	}

	for {
		opts := cmd.Opts
		// Enable timestamps so that `forwardLogs` can parse the logs.
		opts.Timestamps = true
		// If we are reconnecting, set SinceTime so we don't double-print logs.
		if !lastMessageTime.IsZero() {
			// The SinceTime parameter only has second-level resolution, which
			// can result in duplicated logs. We save the exact sinceTime to do
			// some manual filtering later.
			sinceTime = lastMessageTime
			metaSinceTime := metav1.NewTime(lastMessageTime)
			opts.SinceTime = &metaSinceTime
		}

		logsReq := kubeClient.CoreV1().
			Pods(cmd.Config.Auth.KubeNamespace).
			GetLogs(names.PodName(service), &opts)

		logsStream, err := logsReq.Stream()
		if err != nil {
			if !cmd.Opts.Follow {
				return errors.WithContext("start logs stream", err)
			}

			select {
			case <-ctx.Done():
				return context.Canceled
			case <-time.After(5 * time.Second):
				// We might not have a network connection. Try again in a few
				// seconds.
				log.WithField("service", service).WithError(err).Debug("Failed to connect to logs, retrying")
				continue
			case <-podExited:
				printStatusMessage(service, "The container exited.", len(cmd.Services) == 1)
				return errors.WithContext("start logs stream", err)
			}
		}
		defer logsStream.Close()

		reader := bufio.NewReader(logsStream)
	readLoop:
		for {
			message, err := reader.ReadString('\n')

			if err == nil && isOldMessage(message) {
				// Discard this message.
				log.WithField("message", message).Debug("Discarding duplicate message")
				continue
			}

			combinedLogs <- rawLogLine{
				fromContainer: service,
				message:       strings.TrimSuffix(message, "\n"),
				receivedAt:    time.Now(),
				error:         err,
			}

			if err != nil {
				if !cmd.Opts.Follow {
					// Signal to the parent that there will be no more logs for this
					// container, so that the parent can shut down cleanly once all the
					// log streams have ended.
					return errors.WithContext("recv log stream", err)
				}

				select {
				case <-ctx.Done():
					return context.Canceled
				case <-time.After(500 * time.Millisecond):
					// This might have been a transport issue, so if the pod
					// hasn't exited within 500ms, try reconnecting to the logs.
					log.WithField("service", service).WithError(err).Debug("reconnecting after error")
					printStatusMessage(service, "Disconnected from logs, reconnecting..", len(cmd.Services) == 1)
					break readLoop
				case <-podExited:
					printStatusMessage(service, "The container exited.", len(cmd.Services) == 1)
					return errors.WithContext("recv log stream", err)
				}
			}
		}
	}
}

func printStatusMessage(service, message string, hideServiceName bool) {
	servicePrefix := ""
	if !hideServiceName {
		coloredContainer := goterm.Color(service, pickColor(service))
		servicePrefix = fmt.Sprintf("%s - ", coloredContainer)
	}

	fmt.Fprintf(os.Stderr, "%s%s\n", servicePrefix, message)
}

// The logs within a window are guaranteed to be sorted.
// Note that it's still possible for a delayed log to arrive in the next
// window, in which case it will be printed out of order.
const windowSize = 100 * time.Millisecond

// printLogs reads logs from the `rawLogs` in `windowSize` intervals, and
// prints the logs in each window in sorted order.
func printLogs(ctx context.Context, rawLogs <-chan rawLogLine, hideServiceName bool) error {
	var window []rawLogLine
	var flushTrigger <-chan time.Time

	// flush prints the logs in the current window to the terminal.
	flush := func() {
		// Parse the logs in the windows to extract their timestamps.
		var parsedLogs []parsedLogLine
		for _, rawLog := range window {
			if rawLog.error != nil {
				// If we got a message (which might be possible), try to parse
				// it.
				if rawLog.message != "" {
					message, timestamp, err := parseLogLine(rawLog.message)
					if err != nil {
						// Don't warn here, this is reasonable.
						message = rawLog.message
						timestamp = rawLog.receivedAt
					}

					parsedLogs = append(parsedLogs, parsedLogLine{
						fromContainer: rawLog.fromContainer,
						message:       message,
						loggedAt:      timestamp,
					})
				}

				if rawLog.error != io.EOF {
					log.WithError(rawLog.error).Debug("Error in logs stream.")
				}

				continue
			}
			message, timestamp, err := parseLogLine(rawLog.message)

			// If we fail to parse the log's timestamp, revert to sorting based
			// on its receival time.
			if err != nil {
				log.WithField("message", rawLog.message).
					WithField("container", rawLog.fromContainer).
					WithError(err).Warn("Failed to parse timestamp")
				message = rawLog.message
				timestamp = rawLog.receivedAt
			}

			parsedLogs = append(parsedLogs, parsedLogLine{
				fromContainer: rawLog.fromContainer,
				message:       message,
				loggedAt:      timestamp,
			})
		}

		// Sort logs in the window.
		byLogTime := func(i, j int) bool {
			return parsedLogs[i].loggedAt.Before(parsedLogs[j].loggedAt)
		}
		sort.SliceStable(parsedLogs, byLogTime)

		// Print the logs.
		for _, log := range parsedLogs {
			switch {
			case log.formatOverride != "":
				fmt.Fprintf(os.Stdout, "%s", log.formatOverride)

			case hideServiceName:
				fmt.Fprintln(os.Stdout, log.message)

			default:
				coloredContainer := goterm.Color(log.fromContainer, pickColor(log.fromContainer))
				fmt.Fprintf(os.Stdout, "%s › %s\n", coloredContainer, log.message)
			}
		}

		// Clear the buffer now that we've printed its contents.
		window = nil
	}

	defer flush()

	for {
		select {
		case logLine, ok := <-rawLogs:
			if !ok {
				// There won't be any more messages, so we can exit after
				// flushing any unprinted logs.
				return nil
			}

			// Wake up later to flush the buffered lines.
			window = append(window, logLine)
			if flushTrigger == nil {
				flushTrigger = time.After(windowSize)
			}
		case <-flushTrigger:
			flush()
			flushTrigger = nil
		case <-ctx.Done():
			// Finish printing any logs that are still on the channel.
			for {
				select {
				case logLine, ok := <-rawLogs:
					if !ok {
						return nil
					}

					window = append(window, logLine)
				default:
					return nil
				}
			}
		}
	}
}

func parseLogLine(rawMessage string) (string, time.Time, error) {
	logParts := strings.SplitN(rawMessage, " ", 2)
	if len(logParts) != 2 {
		return "", time.Time{}, errors.New("malformed line")
	}

	rawTimestamp := logParts[0]
	timestamp, err := time.Parse(time.RFC3339Nano, rawTimestamp)
	if err != nil {
		// According to the Kubernetes docs, the timestamp might be in the
		// RFC3339 or RFC3339Nano format.
		timestamp, err = time.Parse(time.RFC3339, rawTimestamp)
		if err != nil {
			return "", time.Time{},
				errors.New("parse timestamp")
		}
	}

	message := logParts[1]
	return message, timestamp, nil
}

var colorList = []int{
	goterm.BLUE,
	goterm.CYAN,
	goterm.GREEN,
	goterm.MAGENTA,
	goterm.RED,
	goterm.YELLOW,
}

func pickColor(container string) int {
	hash := fnv.New32()
	_, err := hash.Write([]byte(container))
	if err != nil {
		panic(err)
	}
	idx := hash.Sum32() % uint32(len(colorList))
	return colorList[idx]
}
