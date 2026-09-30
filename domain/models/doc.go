// Package models contains technology-independent models shared by the generic
// Core-to-plugin REST lifecycle use cases. Product settings, capability
// payloads, database drivers and public routes never belong here. Every typed
// error in this package is public-safe: it carries a stable category and never
// serializes transport internals, secret bytes or configuration documents.
package models
