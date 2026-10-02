package aggregator

import "testing"

func TestContainerStoplight(t *testing.T) {
	tests := []struct {
		status string
		want   Stoplight
	}{
		{"running:healthy", StoplightGreen},
		{"running:unknown", StoplightGreen},
		{"running", StoplightGreen},
		{"running:unhealthy", StoplightRed},
		{"running:starting", StoplightYellow},
		{"restarting", StoplightYellow},
		{"exited:unhealthy", StoplightRed},
		{"exited", StoplightRed},
		{"stopped", StoplightRed},
		{"degraded", StoplightRed},
		{"", StoplightGray},
		{"who-knows", StoplightGray},
		{"  Running:Healthy  ", StoplightGreen},
	}
	for _, tc := range tests {
		if got := ContainerStoplight(tc.status); got != tc.want {
			t.Errorf("ContainerStoplight(%q) = %v, want %v", tc.status, got, tc.want)
		}
	}
}

func TestDeployStoplight(t *testing.T) {
	tests := []struct {
		status string
		want   Stoplight
	}{
		{"finished", StoplightGreen},
		{"in_progress", StoplightYellow},
		{"queued", StoplightYellow},
		{"failed", StoplightRed},
		{"cancelled-by-user", StoplightGray}, // spelling: ok (Coolify API status value)
		{"", StoplightGray},
	}
	for _, tc := range tests {
		if got := DeployStoplight(tc.status); got != tc.want {
			t.Errorf("DeployStoplight(%q) = %v, want %v", tc.status, got, tc.want)
		}
	}
}

func TestInFlight(t *testing.T) {
	if !DeployQueued.InFlight() || !DeployInProgress.InFlight() {
		t.Error("queued and in_progress must count as in flight")
	}
	if DeployFinished.InFlight() || DeployFailed.InFlight() {
		t.Error("finished and failed must not count as in flight")
	}
}

func TestAggregate(t *testing.T) {
	if got := Aggregate(); got != StoplightGray {
		t.Errorf("empty Aggregate = %v, want gray", got)
	}
	if got := Aggregate(StoplightGreen, StoplightRed, StoplightYellow); got != StoplightRed {
		t.Errorf("Aggregate = %v, want red", got)
	}
	if got := Aggregate(StoplightGreen, StoplightYellow); got != StoplightYellow {
		t.Errorf("Aggregate = %v, want yellow", got)
	}
}

func TestSortPriorityPutsInProgressFirst(t *testing.T) {
	order := []Stoplight{StoplightYellow, StoplightRed, StoplightGreen, StoplightGray}
	for i := 1; i < len(order); i++ {
		if SortPriority(order[i-1]) >= SortPriority(order[i]) {
			t.Fatalf("priority order broken at %d: %v vs %v", i, order[i-1], order[i])
		}
	}
}

func TestSplitContainerStatus(t *testing.T) {
	state, health := SplitContainerStatus("running:healthy")
	if state != "running" || health != "healthy" {
		t.Errorf("got %q/%q", state, health)
	}
	state, health = SplitContainerStatus("exited")
	if state != "exited" || health != "" {
		t.Errorf("got %q/%q", state, health)
	}
}
