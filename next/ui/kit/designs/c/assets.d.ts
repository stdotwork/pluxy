// The favicon is imported `with { type: "file" }`, like the stylesheets and fonts: the build
// copies it out under a content-hashed name and the import is its address.

declare module "*.svg" {
  const href: string;
  export default href;
}
