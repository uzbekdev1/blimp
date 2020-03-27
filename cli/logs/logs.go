package logs

import (
	"bufio"
	"context"
	"errors"
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
	"github.com/sirupsen/logrus"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/kelda-inc/blimp/cli/authstore"
)

type logsCommand struct {
	kubeClient kubernetes.Interface
	containers []string
	opts       corev1.PodLogOptions
	auth       authstore.Store
}

type rawLogLine struct {
	// Any error that occurred when trying to read logs.
	// If this is non-nil, `message` and `receivedAt` aren't meaningful.
	readError error

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
}

func New() *cobra.Command {
	cmd := &logsCommand{}

	cobraCmd := &cobra.Command{
		Use: "logs",
		Run: func(_ *cobra.Command, args []string) {
			auth, err := authstore.New()
			if err != nil {
				log.WithError(err).Fatal("Failed to parse auth store")
			}

			if auth.AuthToken == "" {
				fmt.Fprintln(os.Stderr, "Not logged in. Please run `blimp login`.")
				return
			}

			kubeClient, _, err := auth.KubeClient()
			if err != nil {
				log.WithError(err).Fatal("Failed to connect to cluster")
			}

			if len(args) == 0 {
				fmt.Fprintf(os.Stderr, "At least one container is required")
				os.Exit(1)
			}

			cmd.auth = auth
			cmd.containers = args
			cmd.kubeClient = kubeClient
			if err := cmd.run(); err != nil {
				log.Fatal(err)
			}
		},
	}

	cobraCmd.Flags().BoolVarP(&cmd.opts.Follow, "follow", "f", false,
		"Specify if the logs should be streamed.")
	cobraCmd.Flags().BoolVarP(&cmd.opts.Previous, "previous", "p", false,
		"If true, print the logs for the previous instance of the container if it crashed.")

	return cobraCmd
}

func (cmd logsCommand) run() error {
	// Exit gracefully when the user Ctrl-C's.
	// The `printLogs` function will return when the context is cancelled,
	// which allows functions defered in this method to run.
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-signalChan
		cancel()
	}()

	var wg sync.WaitGroup
	combinedLogs := make(chan rawLogLine, len(cmd.containers)*32)
	for _, container := range cmd.containers {
		// Enable timestamps so that `forwardLogs` can parse the logs.
		cmd.opts.Timestamps = true
		logsReq := cmd.kubeClient.CoreV1().
			Pods(cmd.auth.KubeNamespace).
			GetLogs(container, &cmd.opts)

		logsStream, err := logsReq.Stream()
		if err != nil {
			return fmt.Errorf("start logs stream: %w", err)
		}
		defer logsStream.Close()

		wg.Add(1)
		go func(container string) {
			forwardLogs(combinedLogs, container, logsStream)
			wg.Done()
		}(container)
	}

	// No more log messages will be written to the channel once all the
	// `forwardLogs` threads finish.
	go func() {
		wg.Wait()
		close(combinedLogs)
	}()

	noColor := len(cmd.containers) == 1
	return printLogs(ctx, combinedLogs, noColor)
}

// forwardLogs forwards each log line from `logsReq` to the `combinedLogs`
// channel.
func forwardLogs(combinedLogs chan<- rawLogLine, container string, logsStream io.ReadCloser) {
	scanner := bufio.NewScanner(logsStream)
	for {
		scanned := scanner.Scan()
		if !scanned {
			// The error will be `nil` if we didn't scan because the stream has
			// ended.
			if err := scanner.Err(); err != nil {
				combinedLogs <- rawLogLine{fromContainer: container, readError: err}
			}
			return
		}

		combinedLogs <- rawLogLine{
			fromContainer: container,
			message:       scanner.Text(),
			receivedAt:    time.Now(),
		}
	}
}

// The logs within a window are guaranteed to be sorted.
// Note that it's still possible for a delayed log to arrive in the next
// window, in which case it will be printed out of order.
const windowSize = 100 * time.Millisecond

// printLogs reads logs from the `rawLogs` in `windowSize` intervals, and
// prints the logs in each window in sorted order.
func printLogs(ctx context.Context, rawLogs <-chan rawLogLine, noColor bool) error {
	var window []rawLogLine
	var flushTrigger <-chan time.Time

	// flush prints the logs in the current window to the terminal.
	flush := func() {
		// Parse the logs in the windows to extract their timestamps.
		var parsedLogs []parsedLogLine
		for _, rawLog := range window {
			message, timestamp, err := parseLogLine(rawLog.message)

			// If we fail to parse the log's timestamp, revert to sorting based
			// on its receival time.
			if err != nil {
				logrus.WithField("message", rawLog.message).
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
		sort.Slice(parsedLogs, byLogTime)

		// Print the logs.
		for _, log := range parsedLogs {
			if noColor {
				fmt.Fprintln(os.Stdout, log.message)
			} else {
				coloredContainer := goterm.Color(log.fromContainer, pickColor(log.fromContainer))
				fmt.Fprintf(os.Stdout, "%s › %s\n", coloredContainer, log.message)
			}
		}

		// Clear the buffer now that we've printed its contents.
		window = nil
	}

	for {
		select {
		case logLine, ok := <-rawLogs:
			if !ok {
				// There won't be any more messages, so we can exit after
				// flushing any unprinted logs.
				flush()
				return nil
			}

			if logLine.readError != nil {
				return fmt.Errorf("read logs for %s: %w", logLine.fromContainer, logLine.readError)
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
			return nil
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
	hash.Write([]byte(container))
	idx := hash.Sum32() % uint32(len(colorList))
	return colorList[idx]
}
