package resources

import (
	"strings"
	"testing"
)

func TestRuntimeProblemsReadLikeAdvice(t *testing.T) {
	for stderr, want := range map[string]string{
		"Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?": "not running",
		"error during connect: open //./pipe/docker_engine: The system cannot find the file specified.":     "not running",
		"permission denied while trying to connect to the Docker daemon socket":                             "isn't allowed",
		"open //./pipe/docker_engine: Access is denied.":                                                    "isn't allowed",
		"something odd happened\nmore":                                                                      "doesn't answer: something odd happened",
	} {
		if got := runtimeProblem("docker", stderr); !strings.Contains(got, want) {
			t.Errorf("%q → %q, want it to say %q", stderr, got, want)
		}
	}
}
