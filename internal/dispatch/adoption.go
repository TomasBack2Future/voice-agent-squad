package dispatch

import (
	"path/filepath"
	"strings"
)

// ReceiverProcess is one local Squad receiver or MCP process as observed in
// the process table. Only identity fields are kept; the full command line is
// never retained or printed.
type ReceiverProcess struct {
	PID             int    `json:"pid"`
	Mode            string `json:"mode"` // terminal-events-listen | mcp
	NativeSession   string `json:"native_session,omitempty"`
	Executable      string `json:"executable"`
	RunsInstalled   bool   `json:"runs_installed"`
	InstalledTarget string `json:"installed_target"`
}

// AdoptionReport separates installation from adoption: the installed target
// is what new launches resolve; each live process reports what it runs.
type AdoptionReport struct {
	InstalledTarget string            `json:"installed_target"`
	Processes       []ReceiverProcess `json:"processes"`
	Adopted         int               `json:"adopted"`
	NotAdopted      int               `json:"not_adopted"`
}

// ParseReceiverProcesses reads `ps -axo pid=,args=` output. A process counts
// only when its executable's base name is "squad" and its subcommand is a
// receiver (`terminal-events listen`) or `mcp`.
func ParseReceiverProcesses(psOutput, installedTarget string) AdoptionReport {
	report := AdoptionReport{InstalledTarget: installedTarget, Processes: []ReceiverProcess{}}
	for _, line := range strings.Split(psOutput, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || filepath.Base(fields[1]) != "squad" {
			continue
		}
		var pid int
		for _, c := range fields[0] {
			if c < '0' || c > '9' {
				pid = -1
				break
			}
			pid = pid*10 + int(c-'0')
		}
		if pid <= 0 {
			continue
		}
		p := ReceiverProcess{PID: pid, Executable: fields[1], InstalledTarget: installedTarget}
		switch {
		case len(fields) >= 4 && fields[2] == "terminal-events" && fields[3] == "listen":
			p.Mode = "terminal-events-listen"
		case fields[2] == "mcp":
			p.Mode = "mcp"
		default:
			continue
		}
		for i := 2; i < len(fields)-1; i++ {
			if fields[i] == "--native-session" {
				p.NativeSession = fields[i+1]
			}
		}
		p.RunsInstalled = installedTarget != "" && filepath.Clean(p.Executable) == filepath.Clean(installedTarget)
		if p.RunsInstalled {
			report.Adopted++
		} else {
			report.NotAdopted++
		}
		report.Processes = append(report.Processes, p)
	}
	return report
}
