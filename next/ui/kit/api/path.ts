// Path helpers the API modules share.

/** An ID as one path segment. */
export const one = (id: string) => encodeURIComponent(id);

/** A publisher/name ID as two path segments. */
export const seg = (s: string) => s.split("/").map(encodeURIComponent).join("/");
