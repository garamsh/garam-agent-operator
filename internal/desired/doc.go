// Package desired is the manager's consumer of the control service's desired
// feed: it pulls each agent's desired state over the C2 wire, renders it into
// the agent's Agent through a Renderer, and reports what it observed and
// rendered.
package desired
