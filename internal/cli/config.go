package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dopeCape/better-nm/internal/config"
	"github.com/dopeCape/better-nm/internal/core"
)

func (a *app) configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Settings: show, get, set, mute/unmute a network, keys, path",
		Long: `Without arguments every setting is printed as "key = value". Settings live
in $XDG_CONFIG_HOME/bnm/config.toml and are applied by the daemon at once.`,
		Args: a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			cfg, err := c.Config(cmd.Context())
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(cfg)
			}
			a.renderConfig(cfg)
			return nil
		},
	}
	get := &cobra.Command{
		Use:   "get <key>",
		Short: "Print one setting",
		Args:  a.exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			cfg, err := c.Config(cmd.Context())
			if err != nil {
				return err
			}
			v, err := cfg.Get(args[0])
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(map[string]string{"key": args[0], "value": v})
			}
			fmt.Fprintln(a.out, v)
			return nil
		},
	}
	set := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Change one setting (lists are comma separated, durations like 30s)",
		Args:  a.exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			cfg, err := c.SetConfig(cmd.Context(), args[0], args[1])
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(cfg)
			}
			v, _ := cfg.Get(args[0])
			a.done("%s = %s", args[0], v)
			return nil
		},
	}
	mute := &cobra.Command{
		Use:   "mute <network-key>",
		Short: "Silence notifications for a network (wifi:<ssid> or <type>:<uuid>)",
		Args:  a.exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.setMuted(cmd, args[0], true)
		},
	}
	unmute := &cobra.Command{
		Use:   "unmute <network-key>",
		Short: "Notify again for a network",
		Args:  a.exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.setMuted(cmd, args[0], false)
		},
	}
	keys := &cobra.Command{
		Use:   "keys",
		Short: "List every settable key with its default",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			def := config.Default()
			if a.jsonOut {
				return a.printJSON(config.Keys())
			}
			for _, k := range config.Keys() {
				v, _ := def.Get(k)
				fmt.Fprintf(a.out, "%s  %s\n", k, a.ui.dim.Render("(default "+quoteIfEmpty(v)+")"))
			}
			return nil
		},
	}
	path := &cobra.Command{
		Use:   "path",
		Short: "Print the config file path",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.jsonOut {
				return a.printJSON(map[string]string{"path": config.Path()})
			}
			fmt.Fprintln(a.out, config.Path())
			return nil
		},
	}
	cmd.AddCommand(get, set, mute, unmute, keys, path)
	return cmd
}

func (a *app) renderConfig(cfg config.Config) {
	section := ""
	for _, k := range config.Keys() {
		sec := strings.SplitN(k, ".", 2)[0]
		if sec != section {
			if section != "" {
				fmt.Fprintln(a.out)
			}
			fmt.Fprintln(a.out, a.ui.header.Render("["+sec+"]"))
			section = sec
		}
		v, _ := cfg.Get(k)
		fmt.Fprintf(a.out, "%s = %s\n", k, quoteIfEmpty(v))
	}
}

func quoteIfEmpty(v string) string {
	if v == "" {
		return `""`
	}
	return v
}

// setMuted rewrites notify.muted_networks through the daemon so it is persisted
// and applied at once. The key is resolved when it names a known SSID.
func (a *app) setMuted(cmd *cobra.Command, key string, mute bool) error {
	ctx := cmd.Context()
	c, err := a.client()
	if err != nil {
		return err
	}
	if !strings.Contains(key, ":") {
		// A bare SSID means wifi:<ssid> when that profile exists.
		if profiles, err := c.Profiles(ctx); err == nil {
			for _, p := range profiles {
				if p.Type == core.ProfileWifi && (p.SSID == key || p.Name == key) {
					key = "wifi:" + p.SSID
					break
				}
			}
		}
		if !strings.Contains(key, ":") {
			return core.Errorf(core.KindInvalid, "network keys look like wifi:<ssid> or ethernet:<uuid>; `bnm status --json` shows the current one", "%q is not a network key", key)
		}
	}
	cfg, err := c.Config(ctx)
	if err != nil {
		return err
	}
	if mute {
		cfg.Mute(key)
	} else if !cfg.Unmute(key) {
		a.done("%s was not muted", key)
		return nil
	}
	if _, err := c.SetConfig(ctx, "notify.muted_networks", strings.Join(cfg.Notify.MutedNetworks, ",")); err != nil {
		return err
	}
	if mute {
		a.done("Muted %s", key)
	} else {
		a.done("Unmuted %s", key)
	}
	return nil
}

func (a *app) notifyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notify",
		Short: "Desktop notifications",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "test",
		Short: "Send a test desktop notification through the daemon",
		Args:  a.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if err := c.NotifyTest(cmd.Context()); err != nil {
				return err
			}
			a.done("sent a test notification")
			return nil
		},
	})
	return cmd
}
