#!/usr/bin/env python3
"""Generate/check synthetic operation-proof examples. Not a production verifier.

Requires Python cryptography. Generation uses an ephemeral P-256 private key;
only its public key and signed assertions are written. --check never generates
keys and does not require network, credentials, a browser, or a running service.
The canonicalizer deliberately supports only this profile's ASCII/string/bool
subset of JCS. It is NOT a general RFC 8785 implementation.
"""
import argparse
import base64
import copy
import hashlib
import json
from pathlib import Path

from cryptography.exceptions import InvalidSignature
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec

DEST = Path(__file__).with_name("vectors.json")
PROFILE = "operation-webauthn-v1-draft"


def b64(raw):
    return base64.urlsafe_b64encode(raw).decode().rstrip("=")


def un64(value):
    raw = base64.urlsafe_b64decode(value + "=" * (-len(value) % 4))
    if b64(raw) != value:
        raise ValueError("noncanonical base64url")
    return raw


def canonical(value):
    def walk(v):
        if isinstance(v, dict):
            for k, x in v.items():
                walk(k)
                walk(x)
        elif isinstance(v, list):
            for x in v:
                walk(x)
        elif isinstance(v, str):
            if not v.isascii() or any(ord(c) < 32 for c in v):
                raise ValueError("fixture profile requires printable ASCII")
        elif not isinstance(v, bool):
            raise ValueError("fixture profile excludes numbers and null")
    walk(value)
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()


def digest(value):
    return "sha256:" + hashlib.sha256(canonical(value)).hexdigest()


def challenge(binding):
    return hashlib.sha256(b"hollis operation proof v1\x00" + canonical(binding)).digest()


def strict(raw):
    def pairs(items):
        out = {}
        for k, v in items:
            if k in out:
                raise ValueError("duplicate JSON member")
            out[k] = v
        return out
    return json.loads(raw, object_pairs_hook=pairs)


def check_case(case, public_key):
    """Fixture oracle only: caller-supplied expected state models trusted stores."""
    operation = case["operation"]
    state = case["trusted_state"]
    response = case["response"]
    participant = case["participant"]
    evidence = response["assurance_evidence"]
    binding = evidence["binding"]
    proof = participant["proof"]
    if response["kind"] != "approval" or response["decision"] != "approved":
        return "decision"
    if digest(operation) != binding["op_digest"]:
        return "operation_binding"
    expected = state["binding"]
    if binding != expected:
        return "trusted_binding"
    if binding["profile"] != PROFILE:
        return "profile"
    if participant["authority"] != binding["authority"] or participant["principal_ref"] != binding["human_subject"]:
        return "human_binding"
    if participant["assurance"] != "webauthn-uv" or proof["scheme"] != PROFILE or proof["key_ref"] != binding["key_ref"]:
        return "proof_descriptor"
    binds = {b["name"]: b["value"] for b in proof["binds"]}
    if len(binds) != len(proof["binds"]) or binds != {k: binding[k] for k in ("op_digest", "item_id", "proof_id", "audience")}:
        return "proof_descriptor"
    if state["requester_human"] == binding["human_subject"]:
        return "self_approval"
    if not state["requester_authorized"] or not state["operation_current"]:
        return "authorization"
    if state["proof_consumed"]:
        return "replay"
    if state["key_revoked"] or not state["trust_fresh"]:
        return "trust"
    now = int(state["now"])
    start, expires = int(binding["issued_at"]), int(binding["expires_at"])
    if start > now or expires <= now or expires - start > 300 or expires <= start or now >= int(operation["expires_at"]):
        return "freshness"
    assertion = evidence["assertion"]
    if assertion["credential_id"] != state["credential_id"]:
        return "credential"
    try:
        client_raw = un64(assertion["client_data_json"])
        client = strict(client_raw)
        auth = un64(assertion["authenticator_data"])
    except (ValueError, TypeError):
        return "encoding"
    if client.get("type") != "webauthn.get" or client.get("challenge") != b64(challenge(binding)):
        return "challenge"
    if client.get("origin") != state["origin"] or client.get("crossOrigin", False) is not False:
        return "origin"
    # Fixtures contain no extensions/attestation data and use a five-byte suffix.
    if len(auth) != 37 or auth[:32] != hashlib.sha256(state["rp_id"].encode()).digest():
        return "rp_id"
    if auth[32] & 5 != 5:
        return "user_verification"
    count = int.from_bytes(auth[33:37], "big")
    if state["check_counter"] and (count or int(state["previous_counter"])) and count <= int(state["previous_counter"]):
        return "counter"
    try:
        public_key.verify(un64(assertion["signature"]), auth + hashlib.sha256(client_raw).digest(), ec.ECDSA(hashes.SHA256()))
    except (InvalidSignature, ValueError):
        return "signature"
    return "accept"


def generate():
    key = ec.generate_private_key(ec.SECP256R1())
    operation = {
        "profile": "privileged-operation-v1-draft", "operation_id": "op-example-1", "revision": "1",
        "requester": {"authority": "workload-example", "subject": "agent-example"},
        "audience": "credential-service-example", "class": "high", "expires_at": "1900000600",
        "authorization_details": [{"type": "credential_registration", "action": "create",
            "principal_id": "producer-example", "owner_id": "human-example", "purpose": "approval producer",
            "credential_expires_at": "1902592000", "overlap_seconds": "0",
            "destination_id": "destination-example", "destination_revision": "1",
            "scopes": ["inbox.approvals_enqueue", "inbox.approvals_read"]}]}
    binding = {"profile": PROFILE, "authority": "human-root-example", "human_subject": "human-example",
        "key_ref": "key-example", "audience": operation["audience"], "op_digest": digest(operation),
        "operation_id": operation["operation_id"], "operation_revision": "1", "item_id": "item-example-1",
        "decision": "approved", "proof_id": "proof-example-1", "nonce": b64(bytes(range(32))),
        "issued_at": "1900000000", "expires_at": "1900000300"}
    state = {"binding": copy.deepcopy(binding), "now": "1900000010", "origin": "https://identity.example.com",
        "rp_id": "identity.example.com", "credential_id": b64(b"synthetic-credential-id"),
        "previous_counter": "0", "check_counter": True, "requester_human": "",
        "requester_authorized": True, "operation_current": True, "key_revoked": False,
        "trust_fresh": True, "proof_consumed": False}
    base = {"operation": operation, "trusted_state": state,
        "response": {"kind": "approval", "decision": "approved", "assurance_evidence": {"binding": binding}},
        "participant": {"principal_ref": "human-example", "authority": "human-root-example", "assurance": "webauthn-uv",
            "proof": {"scheme": PROFILE, "key_ref": "key-example",
                "binds": [{"name": k, "value": binding[k]} for k in ("op_digest", "item_id", "proof_id", "audience")]}}}

    def sign(case, origin=None, flags=5, rp=None, count=1):
        b = case["response"]["assurance_evidence"]["binding"]
        client = canonical({"type": "webauthn.get", "challenge": b64(challenge(b)), "origin": origin or state["origin"], "crossOrigin": False})
        auth = hashlib.sha256((rp or state["rp_id"]).encode()).digest() + bytes([flags]) + count.to_bytes(4, "big")
        sig = key.sign(auth + hashlib.sha256(client).digest(), ec.ECDSA(hashes.SHA256()))
        case["response"]["assurance_evidence"]["assertion"] = {"credential_id": state["credential_id"],
            "client_data_json": b64(client), "authenticator_data": b64(auth), "signature": b64(sig)}

    sign(base)
    cases = []

    def add(name, expected, edit=lambda c: None, **signed):
        c = copy.deepcopy(base)
        c.update(name=name, expected=expected)
        edit(c)
        if signed:
            sign(c, **signed)
        cases.append(c)

    add("valid", "accept")
    add("changed_scope", "operation_binding", lambda c: c["operation"]["authorization_details"][0]["scopes"].append("inbox.admin"))
    add("changed_destination", "operation_binding", lambda c: c["operation"]["authorization_details"][0].update(destination_id="other"))
    add("wrong_item", "trusted_binding", lambda c: c["trusted_state"]["binding"].update(item_id="other"))
    add("wrong_audience", "trusted_binding", lambda c: c["trusted_state"]["binding"].update(audience="other"))
    add("wrong_revision", "trusted_binding", lambda c: c["trusted_state"]["binding"].update(operation_revision="2"))
    add("expired_exact_boundary", "freshness", lambda c: c["trusted_state"].update(now="1900000300"))
    add("future_ceremony", "freshness", lambda c: c["trusted_state"].update(now="1899999999"))
    add("consumed_proof", "replay", lambda c: c["trusted_state"].update(proof_consumed=True))
    add("revoked_key", "trust", lambda c: c["trusted_state"].update(key_revoked=True))
    add("stale_trust", "trust", lambda c: c["trusted_state"].update(trust_fresh=False))
    add("superseded_operation", "authorization", lambda c: c["trusted_state"].update(operation_current=False))
    add("lost_requester_authority", "authorization", lambda c: c["trusted_state"].update(requester_authorized=False))
    add("self_approval", "self_approval", lambda c: c["trusted_state"].update(requester_human="human-example"))
    add("untrusted_human_claim", "human_binding", lambda c: c["participant"].update(principal_ref="other"))
    add("descriptor_mismatch", "proof_descriptor", lambda c: c["participant"]["proof"].update(key_ref="other"))
    add("wrong_credential", "credential", lambda c: c["trusted_state"].update(credential_id="other"))
    add("denied_decision", "decision", lambda c: c["response"].update(decision="denied"))
    add("valid_signature_wrong_origin", "origin", origin="https://other.example.com")
    add("valid_signature_wrong_rp", "rp_id", rp="other.example.com")
    add("valid_signature_without_uv", "user_verification", flags=1)
    add("valid_signature_without_up", "user_verification", flags=4)
    add("counter_regression", "counter", lambda c: c["trusted_state"].update(previous_counter="2"))
    add("counterless_authenticator", "accept", count=0)
    add("consume_reverifies_recorded_assertion", "accept", lambda c: c["trusted_state"].update(previous_counter="1", check_counter=False))
    def changed_nonce(c):
        c["response"]["assurance_evidence"]["binding"]["nonce"] = b64(b"different-nonce-of-32-bytes-long!!")
        c["trusted_state"]["binding"]["nonce"] = c["response"]["assurance_evidence"]["binding"]["nonce"]
    add("changed_nonce", "challenge", changed_nonce)
    add("duplicate_client_data", "encoding", lambda c: c["response"]["assurance_evidence"]["assertion"].update(client_data_json=b64(b'{"type":"webauthn.get","type":"webauthn.get"}')))
    add("corrupt_signature", "signature", lambda c: c["response"]["assurance_evidence"]["assertion"].update(signature=b64(b"invalid DER signature")))
    public = key.public_key().public_bytes(serialization.Encoding.PEM, serialization.PublicFormat.SubjectPublicKeyInfo).decode()
    return {"profile": PROFILE, "synthetic_only": True, "public_key_pem": public,
        "operation_canonical_utf8": canonical(operation).decode(), "operation_digest": digest(operation),
        "binding_canonical_utf8": canonical(binding).decode(), "challenge_base64url": b64(challenge(binding)), "cases": cases}


def check(doc):
    public = serialization.load_pem_public_key(doc["public_key_pem"].encode())
    base = doc["cases"][0]
    assert canonical(base["operation"]).decode() == doc["operation_canonical_utf8"]
    assert digest(base["operation"]) == doc["operation_digest"]
    binding = base["response"]["assurance_evidence"]["binding"]
    assert canonical(binding).decode() == doc["binding_canonical_utf8"]
    assert b64(challenge(binding)) == doc["challenge_base64url"]
    for case in doc["cases"]:
        got = check_case(case, public)
        if got != case["expected"]:
            raise ValueError(f"{case['name']}: expected {case['expected']}, got {got}")
    print(f"Validated {len(doc['cases'])} synthetic cases; public evidence only.")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--generate", action="store_true", help="replace fixtures using a fresh ephemeral test key")
    args = parser.parse_args()
    if args.generate:
        document = generate()
        check(document)
        DEST.write_text(json.dumps(document, indent=2) + "\n")
    else:
        check(strict(DEST.read_bytes()))
