//! The native test's disposable summary tool (`fixture.rs`). Never part of a release: the native
//! test packs it into a signed test package (`native/program.ts`).

mod fixture;

fn main() {
    fixture::main()
}
