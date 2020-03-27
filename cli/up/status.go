package up

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/buger/goterm"
	log "github.com/sirupsen/logrus"

	"github.com/kelda-inc/blimp/pkg/dockercompose"
	"github.com/kelda-inc/blimp/pkg/proto/cluster"
)

type statusPrinter struct {
	services   []string
	tracker    map[string]*tracker
	currStatus map[string]*cluster.ServiceStatus
	hasPrinted bool
	sync.Mutex
}

type tracker struct {
	phase string
	timer int
}

func newStatusPrinter(dc dockercompose.Config) *statusPrinter {
	sp := &statusPrinter{tracker: map[string]*tracker{}}
	for svc := range dc.Services {
		sp.services = append(sp.services, svc)
		sp.tracker[svc] = &tracker{phase: "Pending"}
	}
	sort.Strings(sp.services)

	return sp
}

func (sp *statusPrinter) Run(clusterManager managerClient, authToken string) error {
	// Stop watching the status after we're done printing the status.
	ctx, cancelFn := context.WithCancel(context.Background())
	defer cancelFn()

	statusStream, err := clusterManager.WatchStatus(ctx, &cluster.GetStatusRequest{
		Token: authToken,
	})
	if err != nil {
		return fmt.Errorf("watch status: %w", err)
	}

	go func() {
		for {
			msg, err := statusStream.Recv()
			switch {
			case err == io.EOF:
				return
			case err != nil:
				log.WithError(err).Warn("Failed to get status update")
				return
			}

			sp.Lock()
			sp.currStatus = msg.Status.Services
			sp.Unlock()
		}
	}()

	for {
		if sp.printStatus() {
			fmt.Println(goterm.Color("All containers successfully started", goterm.GREEN))
			return nil
		}
		time.Sleep(1 * time.Second)
	}
}

const donePhase = "Running"

func (sp *statusPrinter) printStatus() bool {
	// Increment the timers on all the statuses.
	for _, tr := range sp.tracker {
		tr.timer++
	}

	// If the state transitioned for any of the services, update its phase, and
	// reset its clock.
	sp.Lock()
	for svc, status := range sp.currStatus {
		tr, ok := sp.tracker[svc]
		if !ok {
			// Ignore services not declared in the Docker Compose file.
			continue
		}

		if tr.phase != status.Phase {
			sp.tracker[svc] = &tracker{phase: status.Phase}
		}
	}
	sp.Unlock()

	// Reset the cursor so that we'll write over the previous status update.
	if sp.hasPrinted {
		goterm.MoveCursorUp(len(sp.services))
		goterm.Flush()
	}

	allReady := true
	out := tabwriter.NewWriter(os.Stdout, 0, 10, 5, ' ', 0)
	defer out.Flush()
	for _, svc := range sp.services {
		tr := sp.tracker[svc]
		var phaseStr string
		if tr.phase != donePhase {
			allReady = false
			ndots := tr.timer + 2
			phaseStr = goterm.Color(tr.phase+strings.Repeat(".", ndots), goterm.YELLOW)
		} else {
			phaseStr = goterm.Color(tr.phase, goterm.GREEN)
		}

		line := fmt.Sprintf("%s\t%s", svc, phaseStr)
		fmt.Fprintln(out, goterm.ResetLine(line))
	}

	sp.hasPrinted = true
	return allReady
}
