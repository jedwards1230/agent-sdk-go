// Package announce is the SDK's announce vocabulary: the single [Payload] type
// an agent server publishes to describe itself, plus the projections
// ([Payload.Validate], JSON and YAML round-tripping) a client needs to read it.
//
// The payload carries six things and nothing else: an identity (server, account,
// display name), a LIST of candidate [Endpoint] values, coarse [SessionSummary]
// rows, an optional opaque [Credential] the SDK never interprets, a REQUIRED
// device.PublicKey, and a capability [Scope].
//
// Two of those are secret-adjacent and are modelled accordingly. A [Credential]
// is a bearer secret, so it hides its value behind [Credential.Reveal] and
// redacts under every format verb while still serializing verbatim. The device
// key is mandatory, so [Payload.Validate] rejects the zero key: end-to-end
// encryption is a hard requirement here, and a server nobody can seal an
// envelope to must not be able to announce itself as if it could.
//
// # What this package deliberately excludes
//
// This package is types only. It opens no socket, browses no mDNS, resolves no
// name, races no candidate, and refreshes no token — it imports neither net nor
// net/http, and it never will. The SDK has no inbound network surface and this
// package does not add one. The discovery machinery (an mDNS browser, a
// rendezvous client, a device-code flow, candidate racing) is networking and
// identity work that lives in a separate module, per the two-gate test in
// CLAUDE.md: describing yourself is vocabulary a second SDK-based application
// needs unchanged; dialing is plumbing a seam can supply.
//
// It also knows nothing about rosters, supervision, or fleets. A roster is an
// application-side projection of many payloads; this package models one server
// describing itself, once.
//
// # Why the endpoints are a list
//
// [Payload.Endpoints] is the extension point that keeps the future
// non-breaking. A server is reachable by several paths at once — a LAN address,
// a tailnet address, a relayed address — and which one works depends on where
// the client is standing, not on what the server knows. Modelling reachability
// as a list of ordered candidates costs nothing today and means a relay path
// arrives later as one more element with a new [EndpointKind], not as a schema
// change. [EndpointKind] is a string-backed open set for the same reason: an
// unrecognized kind survives a round trip instead of failing to decode.
// [Endpoint.Address] is opaque here — this package never parses, resolves,
// validates, or dials it.
//
// # Why this is not in a compose manifest (yet)
//
// The declarative-consumption tenet says every capability should be reachable
// through a compose.Load() manifest, so the question was asked and answered:
// not yet. A compose.Manifest is static configuration an embedder DECLARES
// ahead of time (name, model, provider), whereas an announce payload is runtime
// state a running server EMITS — its endpoints depend on the interfaces it
// found, its session summaries change every turn, and its credential is minted,
// not authored. Putting it in the manifest would ask an embedder to hand-write
// values only the process can know. There is also no consumer: nothing in this
// SDK reads a payload, so a manifest field would wire to nothing. If a server
// later needs to be TOLD its stable identity or its advertised endpoints, the
// declarative half of that is a small manifest block naming those inputs — not
// this payload type — and it should be added then, against a real consumer.
//
// This adds no event.Event kind and changes no existing contract; it is
// additive vocabulary only.
package announce
