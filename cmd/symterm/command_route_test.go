package main

import "testing"

func TestClassifyCommandRoute(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		args []string
		want commandRoute
	}{
		{name: "default empty", args: nil, want: routeDefault},
		{name: "default remote argv", args: []string{"echo", "hi"}, want: routeDefault},
		{name: "help flag", args: []string{"--help"}, want: routeHelp},
		{name: "run", args: []string{"run", "--project-id", "demo", "--", "echo"}, want: routeRun},
		{name: "completion", args: []string{"completion", "bash"}, want: routeCompletion},
		{name: "setup", args: []string{"setup"}, want: routeSetup},
		{name: "admin", args: []string{"admin", "users", "list"}, want: routeAdmin},
		{name: "version", args: []string{"version"}, want: routeVersion},
		{name: "internal authority broker", args: []string{"internal-authority-broker", "--manifest", "x", "--config", "y"}, want: routeAuthorityBroker},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyCommandRoute(tc.args); got != tc.want {
				t.Fatalf("classifyCommandRoute(%q) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}
