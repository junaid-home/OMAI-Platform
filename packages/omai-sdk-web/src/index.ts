// The Go control plane owns the canonical SDK source beside its Protobuf API.
// This workspace package gives the SolidJS monorepo one stable import without
// copying generated descriptors into the frontend tree.
export * from "../../../services/omai-control-plane/sdk/typescript/src/index.js"
