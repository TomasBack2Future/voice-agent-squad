package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// executionLaunchdOptions is the complete, explicit environment the admission
// service needs. A launchd job inherits none of an interactive shell, so every
// value that gh, the ledger and the log paths depend on is pinned here.
type executionLaunchdOptions struct {
	Label, SquadBinary, TokenFile, Listen, WorkingDir, SquadHome, Home, GH, GHConfigDir, Agent, LogDir string
}

func absoluteDir(name, value string) error {
	if !filepath.IsAbs(value) {
		return fmt.Errorf("%s must be an absolute path", name)
	}
	if info, err := os.Stat(value); err != nil || !info.IsDir() {
		return fmt.Errorf("%s must be an existing directory", name)
	}
	return nil
}

func absoluteExecutable(name, value string) error {
	if !filepath.IsAbs(value) {
		return fmt.Errorf("%s must be an absolute path", name)
	}
	if info, err := os.Stat(value); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0100 == 0 {
		return fmt.Errorf("%s must be an executable file", name)
	}
	return nil
}

func (o executionLaunchdOptions) validate() error {
	if strings.TrimSpace(o.Label) == "" || strings.ContainsAny(o.Label, " /\\") || strings.TrimSpace(o.Agent) == "" {
		return errors.New("label and agent are required identifiers")
	}
	if err := absoluteExecutable("--squad-binary", o.SquadBinary); err != nil {
		return err
	}
	if err := absoluteExecutable("--gh", o.GH); err != nil {
		return err
	}
	for name, dir := range map[string]string{"--working-dir": o.WorkingDir, "--squad-home": o.SquadHome, "--home": o.Home, "--gh-config-dir": o.GHConfigDir, "--log-dir": o.LogDir} {
		if err := absoluteDir(name, dir); err != nil {
			return err
		}
	}
	if !filepath.IsAbs(o.TokenFile) {
		return errors.New("--token-file must be an absolute path")
	}
	info, err := os.Stat(o.TokenFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("--token-file must be an owner-only regular file")
	}
	return nil
}

type plistWriter struct{ buf bytes.Buffer }

func (w *plistWriter) text(tag, value string) {
	w.buf.WriteString("<" + tag + ">")
	_ = xml.EscapeText(&w.buf, []byte(value))
	w.buf.WriteString("</" + tag + ">\n")
}

// renderExecutionPlist is pure: it reads only the paths it validates and writes
// nothing. It never reads the token file's contents.
func renderExecutionPlist(o executionLaunchdOptions) (string, error) {
	if err := o.validate(); err != nil {
		return "", err
	}
	// gh's directory leads PATH; the rest is the system default, nothing inherited.
	path := strings.Join([]string{filepath.Dir(o.GH), "/usr/bin", "/bin", "/usr/sbin", "/sbin"}, ":")
	env := [][2]string{
		{"GH_CONFIG_DIR", o.GHConfigDir}, {"HOME", o.Home}, {"PATH", path},
		{"SQUAD_AGENT", o.Agent}, {"SQUAD_HOME", o.SquadHome}, {"SQUAD_NO_AUTO_DAEMON", "1"},
		{"SQUAD_NO_HYGIENE", "1"}, {"SQUAD_SESSION_ID", o.Agent},
	}
	w := &plistWriter{}
	w.buf.WriteString(xml.Header + "<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\">\n<dict>\n")
	w.text("key", "EnvironmentVariables")
	w.buf.WriteString("<dict>\n")
	for _, kv := range env {
		w.text("key", kv[0])
		w.text("string", kv[1])
	}
	w.buf.WriteString("</dict>\n")
	// Restart after a crash, but not after a deliberate failed start such as a
	// listener conflict: a non-zero exit with the same cause must not spin silently.
	w.text("key", "KeepAlive")
	w.buf.WriteString("<dict>\n")
	w.text("key", "SuccessfulExit")
	w.buf.WriteString("<false/>\n</dict>\n")
	w.text("key", "Label")
	w.text("string", o.Label)
	w.text("key", "ProcessType")
	w.text("string", "Background")
	w.text("key", "ProgramArguments")
	w.buf.WriteString("<array>\n")
	for _, a := range []string{o.SquadBinary, "execution", "serve", "--listen", o.Listen, "--token-file", o.TokenFile} {
		w.text("string", a)
	}
	w.buf.WriteString("</array>\n")
	w.text("key", "RunAtLoad")
	w.buf.WriteString("<true/>\n")
	w.text("key", "StandardErrorPath")
	w.text("string", filepath.Join(o.LogDir, "admission.stderr.log"))
	w.text("key", "StandardOutPath")
	w.text("string", filepath.Join(o.LogDir, "admission.stdout.log"))
	w.text("key", "ThrottleInterval")
	w.buf.WriteString("<integer>30</integer>\n")
	w.text("key", "WorkingDirectory")
	w.text("string", o.WorkingDir)
	w.buf.WriteString("</dict>\n</plist>\n")
	return w.buf.String(), nil
}

func newExecutionLaunchdCmd() *cobra.Command {
	o := executionLaunchdOptions{Label: "com.tomasback2future.studio-production-admission", Listen: "127.0.0.1:7788", Agent: "studio-production-admission-service"}
	home, _ := os.UserHomeDir()
	cmd := &cobra.Command{Use: "launchd-plist", Args: cobra.NoArgs, Short: "Print the canonical launchd job for the admission service (pure; installs and restarts nothing)", RunE: func(cmd *cobra.Command, args []string) error {
		if o.Home == "" {
			o.Home = home
		}
		if o.GHConfigDir == "" && o.Home != "" {
			o.GHConfigDir = filepath.Join(o.Home, ".config", "gh")
		}
		if o.GH == "" {
			return errors.New("--gh must name the absolute path of the gh executable")
		}
		out, err := renderExecutionPlist(o)
		if err != nil {
			return err
		}
		_, err = fmt.Fprint(cmd.OutOrStdout(), out)
		return err
	}}
	f := cmd.Flags()
	f.StringVar(&o.Label, "label", o.Label, "launchd label")
	f.StringVar(&o.SquadBinary, "squad-binary", "", "Absolute path of the squad binary to run")
	f.StringVar(&o.TokenFile, "token-file", "", "Absolute path of the owner-only runner token file")
	f.StringVar(&o.Listen, "listen", o.Listen, "Loopback listen address")
	f.StringVar(&o.WorkingDir, "working-dir", "", "Repository whose ledger owns ENV-002")
	f.StringVar(&o.SquadHome, "squad-home", "", "Authoritative Squad home directory")
	f.StringVar(&o.Home, "home", "", "HOME for the service (default: current user's)")
	f.StringVar(&o.GH, "gh", "", "Absolute path of the gh executable")
	f.StringVar(&o.GHConfigDir, "gh-config-dir", "", "gh configuration directory (default: $HOME/.config/gh)")
	f.StringVar(&o.Agent, "agent", o.Agent, "Squad agent and session identity")
	f.StringVar(&o.LogDir, "log-dir", "", "Directory for the service logs")
	return cmd
}
