//! Oh My Pi's YAML files, read and edited in place by the transactions library's YAML editor
//! (YAML 1.2, as Bun's parser that Oh My Pi uses reads them).

use pluxy_program::Error;
use pluxy_transactions::{yaml, Merge};
use serde_json::Value;

/// The program as its refusals name it.
const PRODUCT: &str = "Oh My Pi";

/// The value of a file's bytes (None: no file); see [`yaml::parse`].
pub fn parse(bytes: Option<&[u8]>) -> Result<Value, Error> {
    yaml::parse(bytes, PRODUCT)
}

/// The file's bytes edited to want; see [`yaml::edit`].
pub fn edit(bytes: Option<&[u8]>, current: &Value, want: &Value) -> Result<Vec<u8>, Error> {
    yaml::edit(bytes, current, want, PRODUCT)
}

/// A YAML file's settings for a backup's diff; see [`yaml::leaves`].
pub fn leaves(bytes: &[u8]) -> Option<Vec<(Vec<String>, Value)>> {
    yaml::leaves(bytes, PRODUCT)
}

/// Clear's three-way merge for YAML files ([`yaml::Yamls`]).
pub struct Yamls;

impl Merge for Yamls {
    fn merge(
        &self,
        original: Option<&[u8]>,
        applied: Option<&[u8]>,
        current: Option<&[u8]>,
    ) -> Result<Option<Vec<u8>>, Error> {
        yaml::Yamls(PRODUCT).merge(original, applied, current)
    }
}
