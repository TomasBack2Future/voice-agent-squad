package dispatch

import "testing"

func TestAdoptionReportSeparatesInstallFromLiveUse(t *testing.T) {
	installed := "/rt/0b1bc040/squad"
	ps := ` 101 /rt/299df8db/squad terminal-events listen --native-session fa560c73 --recipient agent-dispatcher --secret-like-value XYZ
 102 /rt/0b1bc040/squad terminal-events listen --native-session 60301275
 103 /rt/299df8db/squad mcp
 104 /usr/bin/python3 runtime_entry.py --native-session fa560c73
 105 /rt/0b1bc040/squad status
 abc /rt/0b1bc040/squad mcp
 106 /tmp/x/notsquad terminal-events listen --native-session zz`
	got := ParseReceiverProcesses(ps, installed)
	if got.Adopted != 1 || got.NotAdopted != 2 || len(got.Processes) != 3 {
		t.Fatalf("%+v", got)
	}
	want := map[int]ReceiverProcess{
		101: {PID: 101, Mode: "terminal-events-listen", NativeSession: "fa560c73", Executable: "/rt/299df8db/squad", RunsInstalled: false, InstalledTarget: installed},
		102: {PID: 102, Mode: "terminal-events-listen", NativeSession: "60301275", Executable: "/rt/0b1bc040/squad", RunsInstalled: true, InstalledTarget: installed},
		103: {PID: 103, Mode: "mcp", Executable: "/rt/299df8db/squad", RunsInstalled: false, InstalledTarget: installed},
	}
	for _, p := range got.Processes {
		if p != want[p.PID] {
			t.Fatalf("process %d: %+v", p.PID, p)
		}
	}
	if empty := ParseReceiverProcesses(ps, ""); empty.Adopted != 0 {
		t.Fatal("unknown installed target must not count as adopted")
	}
}
