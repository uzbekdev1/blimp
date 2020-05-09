package ps

import (
	"github.com/buger/goterm"

	"github.com/kelda-inc/blimp/pkg/proto/cluster"
)

func GetStatusString(svcStatus *cluster.ServiceStatus) (msg string, color int, booted bool) {
	color = goterm.YELLOW
	msg = "Unknown"
	switch svcStatus.Phase {
	case cluster.ServicePhase_INITIALIZING_VOLUMES:
		msg = "Initializing volumes"
	case cluster.ServicePhase_WAIT_DEPENDS_ON:
		msg = "Waiting for dependencies to boot"
	case cluster.ServicePhase_WAIT_SYNC_BIND:
		msg = "Syncing volumes. See progress at http://localhost:8834"
	case cluster.ServicePhase_PENDING:
		msg = "Pending"
	case cluster.ServicePhase_RUNNING:
		msg = "Running"
		color = goterm.GREEN
	case cluster.ServicePhase_EXITED:
		msg = "Exited"
		color = goterm.RED
	}

	if svcStatus.Msg != "" {
		msg += ": " + svcStatus.Msg
	}
	return msg, color, svcStatus.HasStarted
}
