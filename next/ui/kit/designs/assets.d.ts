// A direction's stylesheets and fonts are imported `with { type: "file" }`: the build copies the
// file out under a content-hashed name and the import is its address.

declare module "*.css" {
  const href: string;
  export default href;
}

declare module "*.woff2" {
  const href: string;
  export default href;
}
