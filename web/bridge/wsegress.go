package bridge

import (
	"context"
	"sort"
)

// agentWorkspaceEgress is Fleet.workspaceEgress: it maps the [network] and
// [inject] sections of an agent's resolved workspace onto the egress
// proxy's spec. An agent with no workspace gets the default: open, nothing
// injected.
func (f *Fleet) agentWorkspaceEgress(ctx context.Context, a Agent) (string, EgressSpec, error) {
	name, doc, ok, err := f.AgentWorkspaceDoc(ctx, a)
	if err != nil || !ok {
		return "", EgressSpec{}, err
	}
	return name, egressSpecFor(doc), nil
}

// egressSpecFor translates a workspace doc's network and inject sections.
func egressSpecFor(doc WSDoc) EgressSpec {
	spec := EgressSpec{Mode: doc.Network.Mode, Allow: append([]string(nil), doc.Network.Egress...)}
	hosts := make([]string, 0, len(doc.Inject))
	for host := range doc.Inject {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	for _, host := range hosts {
		in := doc.Inject[host]
		spec.Inject = append(spec.Inject, EgressInjectSpec{Host: host, Header: in.Header, Ref: in.Ref, Format: in.Format})
	}
	return spec
}
