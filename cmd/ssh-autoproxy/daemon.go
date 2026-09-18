package main

import (
	"context"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"ssh-autoproxy/internal/daemon"
)

func newDaemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the background tunnel/proxy daemon in the foreground",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := cmd.Flags().GetString("config")
			if err != nil {
				return err
			}
			verbose, err := cmd.Flags().GetBool("verbose")
			if err != nil {
				return err
			}
			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()
			return daemon.Run(ctx, expandHome(path), verbose)
		},
	}
	cmd.Flags().BoolP("verbose", "v", false, "log each SOCKS5 request handled by the tunnel")
	return cmd
}
