package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"text/tabwriter"
)

// runProbe inventories the bridge, or dumps one resource type verbatim.
//
// It exists because firmware and new bridge models can expose
// resource types this client does not model yet. Rather than guessing, look.
func (a *App) runProbe(args []string) error {
	fs, cfgPath := a.newFlagSet("probe", "probe [flags] [resource-type]")
	if err := fs.Parse(args); err != nil {
		return ErrUsage
	}

	cfg, err := a.loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	client, err := a.client(cfg)
	if err != nil {
		return err
	}

	ctx, cancel := a.context()
	defer cancel()

	// With a type argument, dump that type's raw JSON for inspection.
	if rtype := fs.Arg(0); rtype != "" {
		raw, err := client.RawType(ctx, rtype)
		if err != nil {
			return err
		}
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, raw, "", "  "); err != nil {
			_, err = a.Out.Write(raw)
			return err
		}
		pretty.WriteByte('\n')
		_, err = a.Out.Write(pretty.Bytes())
		return err
	}

	resources, err := client.AllResources(ctx)
	if err != nil {
		return err
	}

	counts := map[string]int{}
	samples := map[string]string{}
	for _, r := range resources {
		counts[r.Type]++
		if samples[r.Type] == "" && r.Metadata.Name != "" {
			samples[r.Type] = r.Metadata.Name
		}
	}
	types := make([]string, 0, len(counts))
	for t := range counts {
		types = append(types, t)
	}
	sort.Strings(types)

	tw := tabwriter.NewWriter(a.Out, 0, 8, 2, ' ', 0)
	fmt.Fprintf(tw, "TYPE\tCOUNT\tEXAMPLE\n")
	for _, t := range types {
		fmt.Fprintf(tw, "%s\t%d\t%s\n", t, counts[t], samples[t])
	}
	fmt.Fprintf(tw, "\n%d resources across %d types\n", len(resources), len(types))
	fmt.Fprintln(tw, "run 'hue probe <type>' to dump one type's raw JSON")
	return tw.Flush()
}
