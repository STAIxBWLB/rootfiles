package tenant

import (
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"strings"
	"time"
)

// OpsActions are what a tenant's operators may run through
// `sudo rootfiles tenant ops`.
var OpsActions = []string{"status", "logs", "gpu-heal", "restart", "reset"}

// AuthorizeOps allows root, and an account sudo identified as one of the
// tenant's operators, to run an operator action.
func AuthorizeOps(s *Spec, actor string, viaSudo bool, action string) error {
	allowed := false
	for _, a := range OpsActions {
		allowed = allowed || a == action
	}
	if !allowed {
		return fmt.Errorf("action %q is not an operator action (allowed: %s)", action, strings.Join(OpsActions, ", "))
	}
	if viaSudo && !s.HasOperator(actor) {
		return fmt.Errorf("%s is not an operator of tenant %s", actor, s.Name)
	}
	return nil
}

// Actor names who runs the command: the sudo caller, else the current user.
func Actor() (name string, viaSudo bool) {
	if u := os.Getenv("SUDO_USER"); u != "" && os.Geteuid() == 0 && u != "root" {
		return u, true
	}
	if u, err := user.Current(); err == nil {
		return u.Username, false
	}
	return fmt.Sprintf("uid:%d", os.Getuid()), false
}

// AuditPath receives one JSON line per mutating tenant command.
var AuditPath = "/var/log/rootfiles-tenant.jsonl"

// Audit appends a record. It never carries secrets: callers pass the
// action name and an error summary only.
func Audit(actor, tenant, action, result string) error {
	line, err := json.Marshal(struct {
		Time   string `json:"time"`
		Actor  string `json:"actor"`
		Tenant string `json:"tenant"`
		Action string `json:"action"`
		Result string `json:"result"`
	}{time.Now().UTC().Format(time.RFC3339), actor, tenant, action, result})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(AuditPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}
