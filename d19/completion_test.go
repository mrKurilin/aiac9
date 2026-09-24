package main

import (
	"reflect"
	"strings"
	"testing"
)

func completionValues(items []completion) []string {
	values := make([]string, len(items))
	for index, item := range items {
		values[index] = item.Value
	}
	return values
}

func TestAutocompleteRevealsOneCommandLevelAtATime(t *testing.T) {
	cases := []struct {
		input string
		want  []string
	}{
		{"/", []string{"/info", "/mcp"}},
		{"/m", []string{"/mcp"}},
		{"/mcp", []string{"/mcp list", "/mcp mrkgitlab", "/mcp mrkscheduler", "/mcp mrkpipeline"}},
		{"/mcp ", []string{"/mcp list", "/mcp mrkgitlab", "/mcp mrkscheduler", "/mcp mrkpipeline"}},
		{"/mcp m", []string{"/mcp mrkgitlab", "/mcp mrkscheduler", "/mcp mrkpipeline"}},
		{"/mcp mrkpipeline", []string{"/mcp mrkpipeline tools", "/mcp mrkpipeline run", "/mcp mrkpipeline call"}},
		{"/mcp mrkscheduler", []string{"/mcp mrkscheduler tools", "/mcp mrkscheduler add", "/mcp mrkscheduler mrs", "/mcp mrkscheduler list", "/mcp mrkscheduler summary", "/mcp mrkscheduler cancel"}},
		{"/mcp mrkgitlab", []string{"/mcp mrkgitlab tools", "/mcp mrkgitlab mrs", "/mcp mrkgitlab call"}},
		{"/mcp mrkgitlab ", []string{"/mcp mrkgitlab tools", "/mcp mrkgitlab mrs", "/mcp mrkgitlab call"}},
		{"/mcp mrkgitlab m", []string{"/mcp mrkgitlab mrs"}},
		{"/invariant", []string{"/invariant add", "/invariant remove", "/invariant clear"}},
		{"/plan-mode", []string{"/plan-mode enable", "/plan-mode disable"}},
	}
	for _, test := range cases {
		t.Run(test.input, func(t *testing.T) {
			if got := completionValues(commandCompletions(test.input)); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("got=%v want=%v", got, test.want)
			}
		})
	}
}

func TestEveryTerminalCommandIsReachableInAutocomplete(t *testing.T) {
	for _, command := range slashCommands {
		if len(directCommandChildren(command.Value)) > 0 {
			continue
		}
		parts := strings.Fields(command.Value)
		input := command.Value
		if len(parts) > 1 {
			input = strings.Join(parts[:len(parts)-1], " ") + " " + parts[len(parts)-1][:1]
		}
		options := commandCompletions(input)
		found := false
		for _, option := range options {
			if option.Value == command.Value {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing completion for %q from %q: %v", command.Value, input, completionValues(options))
		}
	}
	if len(commandCompletions("/gitlab")) != 0 {
		t.Fatal("obsolete /gitlab command remains")
	}
	if !strings.Contains(terminalHelp, "/mcp mrkgitlab mrs") {
		t.Fatal("help omits GitLab MR command")
	}
}
