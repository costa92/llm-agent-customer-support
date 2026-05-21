// Package flowrunner is the customer-support service's bridge to
// github.com/costa92/llm-agent-flow. It demonstrates how a downstream
// app composes a flow Engine with the shared OTel TracerProvider via
// llm-agent-otel/otelflow, so per-node spans show up in the same
// trace tree as chat / tool / RAG spans.
//
// Scope is intentionally narrow at v0.2.3: load flow JSON, compile
// with a caller-supplied NodeRegistry + tools, wrap with otelflow,
// and run. Persistence, HTTP exposure, and a real production flow
// catalog stay in flowd (the llm-agent-flow service binary); this
// package is the in-process composition example.
package flowrunner
