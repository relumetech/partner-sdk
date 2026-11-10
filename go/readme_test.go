package relume

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func readme(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestReadmeShowsEveryOptionInItsExample(t *testing.T) {
	example := regexp.MustCompile("(?ms)^relume\\.New\\(relume\\.Options\\{\n(.*?)\n\\}\\)").FindStringSubmatch(readme(t))
	if example == nil {
		t.Fatal("no relume.New(relume.Options{ … }) example")
	}
	// The callbacks have their own section.
	callbacks := map[string]bool{"UserIDFromRequest": true, "OnGetUser": true, "OnGetCredits": true, "OnChargeCredits": true, "OnReverseCharge": true}
	options := reflect.TypeOf(Options{})
	for i := range options.NumField() {
		name := options.Field(i).Name
		if callbacks[name] {
			continue
		}
		if !regexp.MustCompile(`(?m)^\t` + name + `:`).MatchString(example[1]) {
			t.Errorf("the Options example is missing %s", name)
		}
	}
}

func TestReadmeDocumentsEveryCallbackAndHandler(t *testing.T) {
	text := readme(t)
	for _, member := range []string{
		"UserIDFromRequest", "OnGetUser", "OnGetCredits", "OnChargeCredits", "OnReverseCharge",
		"client.Handler()", "client.SessionHandler()", "client.CallsHandler()",
	} {
		if !strings.Contains(text, member) {
			t.Errorf("README is missing %s", member)
		}
	}
}
