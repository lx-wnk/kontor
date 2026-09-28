package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lx-wnk/kontor/server/internal/plugin"
)

// newModuleCmd installs modules from a git repository into the module root.
//
// Git rather than a registry: a module's version is a tag, an update is a
// checkout, and there is no service to run. The server picks the module up on
// its next boot — installing does not reach into a running server, so nothing
// here can disturb one.
func newModuleCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "module", Short: "Install and update modules from a git repository"}
	cmd.PersistentFlags().String("dir", "", "Module root (default: $KONTOR_PLUGIN_DIR)")

	add := &cobra.Command{
		Use:   "add <git-url>[@ref]",
		Short: "Clone a module into the module root",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := moduleRoot(cmd)
			if err != nil {
				return err
			}
			url, ref := splitModuleRef(args[0])
			dest := filepath.Join(root, moduleDirName(url))
			if _, err := os.Stat(dest); err == nil {
				return fmt.Errorf("%s already exists — use `kontor module update %s`", dest, filepath.Base(dest))
			}
			if err := os.MkdirAll(root, 0o700); err != nil {
				return fmt.Errorf("create module root: %w", err)
			}
			if err := runGit(cmd, "", "clone", url, dest); err != nil {
				return err
			}
			if ref != "" {
				if err := runGit(cmd, dest, "checkout", ref); err != nil {
					return err
				}
			}
			return reportModule(cmd, dest)
		},
	}

	update := &cobra.Command{
		Use:   "update <name> [ref]",
		Short: "Fetch and check out a module's ref",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := moduleRoot(cmd)
			if err != nil {
				return err
			}
			dest := filepath.Join(root, args[0])
			if _, err := os.Stat(dest); err != nil {
				return fmt.Errorf("%s is not installed", args[0])
			}
			if err := runGit(cmd, dest, "fetch", "--tags", "--prune"); err != nil {
				return err
			}
			if len(args) == 2 {
				if err := runGit(cmd, dest, "checkout", args[1]); err != nil {
					return err
				}
			}
			return reportModule(cmd, dest)
		},
	}

	cmd.AddCommand(add, update)
	return cmd
}

// moduleRoot is where modules live. It is required rather than guessed: the
// server reads modules from exactly one configured directory, and installing
// into a different one would produce a module that is never loaded.
func moduleRoot(cmd *cobra.Command) (string, error) {
	if dir, _ := cmd.Flags().GetString("dir"); dir != "" {
		return dir, nil
	}
	if dir := os.Getenv("KONTOR_PLUGIN_DIR"); dir != "" {
		return dir, nil
	}
	if dir := os.Getenv("DASHBOARD_PLUGIN_DIR"); dir != "" {
		return dir, nil
	}
	return "", fmt.Errorf("no module root: set KONTOR_PLUGIN_DIR or pass --dir")
}

// splitModuleRef separates a trailing @ref from the URL, leaving the scp-style
// `git@host:owner/repo` form alone — its @ is part of the address.
func splitModuleRef(arg string) (url, ref string) {
	at := strings.LastIndex(arg, "@")
	if at <= 0 {
		return arg, ""
	}
	if strings.Contains(arg[at:], ":") || strings.Contains(arg[at:], "/") {
		return arg, ""
	}
	return arg[:at], arg[at+1:]
}

// moduleDirName is the repository's own name, without .git.
func moduleDirName(url string) string {
	name := url
	if idx := strings.LastIndexAny(name, "/:"); idx >= 0 {
		name = name[idx+1:]
	}
	return strings.TrimSuffix(name, ".git")
}

func runGit(cmd *cobra.Command, dir string, args ...string) error {
	git := exec.Command("git", args...) //nolint:gosec // the arguments are this command's own, not a user-supplied string
	git.Dir = dir
	git.Stdout = cmd.OutOrStdout()
	git.Stderr = cmd.ErrOrStderr()
	if err := git.Run(); err != nil {
		return fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// reportModule reads the installed manifest and says what was installed, or why
// the server will refuse it. Refusing at install time is the whole point of the
// contract: the alternative is a module that fails at boot, in a log nobody is
// reading yet.
func reportModule(cmd *cobra.Command, dir string) error {
	raw, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		return fmt.Errorf("%s has no plugin.json — it is not a module", dir)
	}
	var desc plugin.Descriptor
	if err := json.Unmarshal(raw, &desc); err != nil {
		return fmt.Errorf("%s has an unreadable plugin.json: %w", dir, err)
	}
	if err := desc.Validate(); err != nil {
		return fmt.Errorf("this server will refuse the module: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "installed %s (%s) at %s — it loads on the next server start\n", desc.Name, desc.ID, dir)
	return nil
}
