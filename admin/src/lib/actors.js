/**
 * Who acted, when it was a key rather than a person.
 *
 * Since M71 an API key is recorded as `key` and an agent key as `agent`, each
 * with the key's own id, and the row's "email" is the name the owner gave the
 * key: "api key: Till", "agent key: Order desk". Every screen that prints an
 * actor asks here first, so the feed, a record's history, an order's timeline
 * and a company's read "Key · Till" and "Agent · Order desk" the same way.
 *
 * Takes an audit row (`actor_kind`, `actor_email`, `actor_label`) or an entry
 * from the Who filter (`kind`, `email`, `label`). Returns "" for any other
 * kind, so a caller falls through to its own words for a person, a token or
 * the store. Rows written before M71 said `operator` for a key, and keep
 * reading as the "api key: …" they recorded.
 */
const PREFIX = { key: "api key: ", agent: "agent key: " };

export function keyActorName(entry) {
    const kind = entry?.actor_kind ?? entry?.kind;
    if (kind !== "key" && kind !== "agent") return "";
    const email = entry?.actor_email ?? entry?.email ?? "";
    const label = entry?.actor_label ?? entry?.label ?? "";
    const prefix = PREFIX[kind];
    const name = (email.startsWith(prefix) ? email.slice(prefix.length) : email) || "unnamed";
    if (kind === "key") return `Key · ${name}`;
    // The run label is what tells one night's work from another's.
    return label ? `Agent · ${name} (${label})` : `Agent · ${name}`;
}
