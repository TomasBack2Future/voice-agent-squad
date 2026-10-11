package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/zsiec/squad/internal/dispatch"
)

func decodeLegacyStopInput(raw []byte) (dispatch.LegacyStopInput, error) {
	var input dispatch.LegacyStopInput
	if len(raw) > 65536 {
		return input, errors.New("legacy native preparation exceeds 64KiB bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return input, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return input, errors.New("one exact legacy preparation required")
	}
	return input, nil
}

func legacyWorkerStopCommands() []*cobra.Command {
	var path string
	prepare := &cobra.Command{Use: "worker-stop-prepare", Short: "Original-native bounded stop preparation and persistent fence; does not stop a process", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		raw, err := io.ReadAll(io.LimitReader(file, 65537))
		if err != nil {
			return err
		}
		input, err := decodeLegacyStopInput(raw)
		if err != nil {
			return err
		}
		if os.Getenv("SQUAD_NATIVE_SESSION_ID") != input.Handoff.Expected.WorkerThreadID {
			return errors.New("original native runtime identity required; controller cannot prepare for the owner")
		}
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		receipt, err := dispatch.New(bc.db, bc.repoID, nil).PrepareLegacyWorkerStop(cmd.Context(), bc.agentID, input)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(receipt)
	}}
	prepare.Flags().StringVar(&path, "request", "", "exact original-native preparation file; no stop/process receipt imports")
	_ = prepare.MarkFlagRequired("request")
	get := &cobra.Command{Use: "worker-stop-get STOP_ID", Short: "Read the immutable native preparation and actual host observation", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		receipt, err := dispatch.New(bc.db, bc.repoID, nil).LegacyWorkerStopReceipt(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(receipt)
	}}
	var controllerNative string
	var controllerEpoch int64
	observe := &cobra.Command{Use: "worker-stop-observe STOP_ID", Short: "Current-controller host verification of native, tools, renewal and external custody; no process stopping", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if os.Getenv("SQUAD_NATIVE_SESSION_ID") != controllerNative {
			return errors.New("actual controller native runtime required")
		}
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		receipt, err := dispatch.New(bc.db, bc.repoID, nil).ObserveLegacyWorkerStop(cmd.Context(), bc.agentID, dispatch.ControllerBinding{Actor: bc.agentID, Native: controllerNative, Epoch: controllerEpoch}, args[0])
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(receipt)
	}}
	observe.Flags().StringVar(&controllerNative, "native-session", "", "actual current controller native")
	observe.Flags().Int64Var(&controllerEpoch, "epoch", 0, "actual controller epoch")
	var workerNative string
	check := &cobra.Command{Use: "worker-native-check", Short: "Read the persistent legacy native fence before tool execution or resume", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if os.Getenv("SQUAD_NATIVE_SESSION_ID") != workerNative {
			return errors.New("exact Worker native runtime required")
		}
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		if err = dispatch.New(bc.db, bc.repoID, nil).CheckWorkerNative(cmd.Context(), bc.agentID, workerNative); err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"state": "eligible", "native_session": workerNative})
	}}
	check.Flags().StringVar(&workerNative, "native-session", "", "actual Worker native")
	var hookNative, hookConfig string
	hook := &cobra.Command{Use: "worker-native-hook-check", Short: "Kernel-qualified synchronous hook eligibility and execution observation; no receipt imports", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if os.Getenv("SQUAD_NATIVE_SESSION_ID") != hookNative {
			return errors.New("exact original hook native runtime required")
		}
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		if err := dispatch.New(bc.db, bc.repoID, nil).CheckWorkerNativeHook(cmd.Context(), bc.agentID, hookNative, hookConfig); err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"state": "eligible", "native_session": hookNative})
	}}
	hook.Flags().StringVar(&hookNative, "native-session", "", "actual original hook native")
	hook.Flags().StringVar(&hookConfig, "hook-config", "", "actual selected native hook configuration")
	_ = hook.MarkFlagRequired("native-session")
	_ = hook.MarkFlagRequired("hook-config")
	hookGet := &cobra.Command{Use: "worker-native-hook-get NATIVE", Short: "Read actual kernel-observed native hook identity for controller admission", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		observation, err := dispatch.New(bc.db, bc.repoID, nil).WorkerNativeHookObservation(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(observation)
	}}
	var workspace string
	source := &cobra.Command{Use: "worker-stop-source-check STOP_ID", Short: "Check the actual retained legacy worktree snapshot before replacement startup", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		bc, err := bootClaimContext(cmd.Context())
		if err != nil {
			return err
		}
		defer bc.Close()
		hash, err := dispatch.New(bc.db, bc.repoID, nil).CheckLegacyWorkerSource(cmd.Context(), args[0], workspace)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"state": "verified", "workspace_sha256": hash})
	}}
	source.Flags().StringVar(&workspace, "workspace", "", "exact retained source root")
	_ = source.MarkFlagRequired("workspace")
	return []*cobra.Command{prepare, get, observe, check, hook, hookGet, source}
}
