package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"

	"github.com/spf13/cobra"
)

func newTasksCmd(cfg *CLIConfig) *cobra.Command {
	var jsonOutput bool
	var statusFilter string

	cmd := &cobra.Command{
		Use:   "tasks",
		Short: "Task pipeline commands",
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List tasks",
		RunE: func(cmd *cobra.Command, args []string) error {
			cl := newClient(*cfg)
			path := "/api/tasks"
			if statusFilter != "" {
				params := url.Values{}
				params.Set("stage", statusFilter)
				path += "?" + params.Encode()
			}
			var tasks []map[string]any
			if err := cl.get(path, &tasks); err != nil {
				return err
			}
			if jsonOutput {
				return printJSON(tasks)
			}
			tw := newTabWriter()
			fmt.Fprintln(tw, "ID\tSLUG\tSTAGE\tPRIORITY\tTITLE")
			for _, t := range tasks {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
					strField(t, "id"),
					strField(t, "slug"),
					strField(t, "currentStage"),
					strField(t, "priority"),
					strField(t, "title"),
				)
			}
			return tw.Flush()
		},
	}
	listCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output raw JSON")
	listCmd.Flags().StringVar(&statusFilter, "stage", "", "Filter by stage (e.g. implementation)")

	cancelCmd := &cobra.Command{
		Use:   "cancel <id>",
		Short: "Cancel a task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl := newClient(*cfg)
			if err := cl.post("/api/tasks/"+args[0]+"/cancel", nil, nil); err != nil {
				return err
			}
			fmt.Println("Task cancelled.")
			return nil
		},
	}

	var reference string
	doneCmd := &cobra.Command{
		Use:   "done <id>",
		Short: "Mark a task done whose work landed outside the pipeline",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl := newClient(*cfg)
			if err := cl.post("/api/tasks/"+args[0]+"/complete", map[string]string{"reference": reference}, nil); err != nil {
				return err
			}
			fmt.Println("Task marked done.")
			return nil
		},
	}
	doneCmd.Flags().StringVar(&reference, "ref", "", "PR or commit the work landed in (e.g. owner/repo#123)")

	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a task from a JSON file",
		Long:  "Read task spec from --file or stdin. JSON must include at least: slug, title, cwd.",
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath, _ := cmd.Flags().GetString("file")
			var data []byte
			var err error
			if filePath != "" {
				data, err = os.ReadFile(filePath)
			} else {
				data, err = io.ReadAll(os.Stdin)
			}
			if err != nil {
				return fmt.Errorf("read input: %w", err)
			}
			var body map[string]any
			if err := json.Unmarshal(data, &body); err != nil {
				return fmt.Errorf("parse JSON: %w", err)
			}
			cl := newClient(*cfg)
			var created map[string]any
			if err := cl.post("/api/tasks", body, &created); err != nil {
				return err
			}
			return printJSON(created)
		},
	}
	createCmd.Flags().String("file", "", "Path to JSON spec file (default: stdin)")

	logsCmd := &cobra.Command{
		Use:   "logs <id>",
		Short: "Stream task stage output via SSE (filtered from global broadcast)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			taskID := args[0]
			cl := newClient(*cfg)
			if err := cl.stream(cmd.Context(), "/api/tasks/stream", func(data []byte) {
				var event map[string]any
				if err := json.Unmarshal(data, &event); err != nil {
					return
				}
				if event["taskId"] != taskID {
					return
				}
				fmt.Println(string(data))
			}); err != nil {
				return fmt.Errorf("stream error: %w", err)
			}
			return nil
		},
	}

	cmd.AddCommand(listCmd, cancelCmd, doneCmd, createCmd, logsCmd)
	return cmd
}
