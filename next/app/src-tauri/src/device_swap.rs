//! Swapping an admitted native tool's release (`../app-host/README.md`, states and lifetime):
//! an update or a rollback of a native tool the person enabled. The release that replaces it is
//! staged into the store, kept there while the swap is under way, and admitted only by its own
//! native confirmation; the program host starts and describes it before it is committed
//! (`commit_swap`), while the active release stays active, enabled and admitted. A swap that
//! fails or is cancelled (`end_swap`) leaves the catalog as it was. Either way the release that
//! replaces another must read the state version the other writes.

use super::*;

/// A native release waiting to replace package `id`'s active, admitted release: an update, or
/// the last good release for a rollback. Only the catalog builds one.
#[derive(Debug)]
pub struct Swap {
    pub id: String,
    release: Release,
    source: Source,
    /// The active release it replaces, by digest and install time.
    from: (String, u64),
    from_version: String,
    rollback: bool,
    admitted: Admitted,
    /// The update's store folder, kept from collection until the swap ends.
    held: Option<String>,
}

impl Swap {
    /// The record the swap's native confirmation shows and the program host starts.
    pub fn admitted(&self) -> &Admitted {
        &self.admitted
    }

    /// The version of the active release it replaces.
    pub fn from_version(&self) -> &str {
        &self.from_version
    }

    /// Checks the release's installed files, without the catalog: hash them outside its lock.
    pub fn verify(&self) -> Result<(), String> {
        verify(&self.admitted.dir, &self.release)?;
        admission::verify_tree(&self.admitted.dir, &self.release.tree)
    }
}

/// Whether native release to can replace native release from: it reads the state version from
/// writes. A release that is not native has no state.
pub(super) fn state_readable(id: &str, from: &Release, to: &Release) -> Result<(), String> {
    let (Some(f), Some(t)) = (&from.state, &to.state) else {
        return Ok(());
    };
    if t.readable_versions.contains(&f.version) {
        return Ok(());
    }
    Err(format!(
        "{id} {} cannot read the state {id} {} writes (version {}; it reads {:?})",
        to.version, from.version, f.version, t.readable_versions
    ))
}

impl Catalog {
    /// Whether a change of package id to release r replaces an admitted native tool, and so is
    /// a swap: id is a native tool the person enabled and confirmed, and r is native too.
    pub fn swaps(&self, id: &str, r: &Release) -> bool {
        !self.safe_mode
            && r.tier == "native"
            && self
                .packages
                .get(id)
                .is_some_and(|p| p.active.tier == "native" && p.authorized())
    }

    /// The admitted package id, ready to be swapped.
    fn swapping(&self, id: &str) -> Result<&Package, String> {
        let p = self
            .packages
            .get(id)
            .ok_or_else(|| format!("{id} is not installed on this device"))?;
        if !self.swaps(id, &p.active) {
            return Err(format!("{id} is not an enabled native tool"));
        }
        Ok(p)
    }

    fn swap(
        &self,
        id: &str,
        release: Release,
        source: Source,
        rollback: bool,
        roots: &dyn Roots,
    ) -> Result<Swap, String> {
        let p = self.swapping(id)?;
        state_readable(id, &p.active, &release)?;
        let (admitted, _) = self.release_record(id, p, &release, roots)?;
        Ok(Swap {
            id: id.into(),
            from: (p.active.sha256.clone(), p.active.installed_at),
            from_version: p.active.version.clone(),
            release,
            source,
            rollback,
            admitted,
            held: None,
        })
    }

    /// An update c of admitted native tool c.id, as a swap: its files go to the store (kept
    /// there until `commit_swap` or `end_swap`); nothing else changes.
    pub fn swap_update(&mut self, c: Candidate, roots: &dyn Roots) -> Result<Swap, String> {
        let result = self.swap_update_inner(&c, roots);
        // The staged folder is either in the store now or no longer needed.
        if let Err(e) = remove_tree(&c.staged) {
            self.cleanup.push(format!("could not delete {e}"));
        }
        if result.is_err() {
            self.collect();
        }
        result
    }

    fn swap_update_inner(&mut self, c: &Candidate, roots: &dyn Roots) -> Result<Swap, String> {
        let id = &c.id;
        let p = self.swapping(id)?;
        if !p.source.same(&c.source) {
            return Err(format!(
                "{id} is installed from {} {}; remove it first to install it from {} {}",
                p.source.kind, p.source.name, c.source.kind, c.source.name
            ));
        }
        if p.active.sha256 == c.release.sha256 {
            return Err(format!("{id} {} is already installed", c.release.version));
        }
        state_readable(id, &p.active, &c.release)?;
        self.place(c)?;
        self.holding.insert(c.release.sha256.clone());
        let mut s = self.swap(id, c.release.clone(), c.source.clone(), false, roots)?;
        s.held = Some(c.release.sha256.clone());
        Ok(s)
    }

    /// A rollback of admitted native tool id to its last good release, as a swap. Check its
    /// files with `Swap::verify`, without the catalog's lock.
    pub fn swap_rollback(&mut self, id: &str, roots: &dyn Roots) -> Result<Swap, String> {
        let p = self.swapping(id)?;
        let last = p
            .last_good
            .clone()
            .ok_or_else(|| format!("{id} has no earlier release to roll back to"))?;
        let source = p.source.clone();
        self.swap(id, last, source, true, roots)
    }

    /// Makes the swap's release active, enabled and admitted, as its native confirmation
    /// confirmed, keeping the release it replaces as the last good one; only while that release
    /// is still the active, admitted one and the swap's record still checks out. Either way the
    /// swap ends.
    pub fn commit_swap(&mut self, s: Swap, roots: &dyn Roots) -> Result<(), String> {
        let result = self.commit_swap_inner(&s, roots);
        self.end_swap(s);
        result
    }

    fn commit_swap_inner(&mut self, s: &Swap, roots: &dyn Roots) -> Result<(), String> {
        let id = &s.id;
        let p = self.swapping(id)?;
        if (&p.active.sha256, p.active.installed_at) != (&s.from.0, s.from.1)
            || (s.rollback && p.last_good.as_ref() != Some(&s.release))
        {
            return Err(format!("{id} changed during the swap; try again"));
        }
        let (now, _) = self.release_record(id, p, &s.release, roots)?;
        if now != s.admitted {
            return Err(format!("{id} changed during the swap; try again"));
        }
        let previous = p.clone();
        let p = self.packages.get_mut(id).unwrap();
        let replaced = std::mem::replace(&mut p.active, s.release.clone());
        p.last_good = Some(replaced);
        // The same source; a local release's file name labels the new release.
        p.source = s.source.clone();
        p.enabled = true;
        p.admission = Some(Admission::of(&s.admitted));
        if let Err(e) = self.save() {
            self.packages.insert(id.clone(), previous);
            return Err(e);
        }
        Ok(())
    }

    /// Ends a swap that did not commit: an update's folder leaves the store unless a release
    /// names it.
    pub fn end_swap(&mut self, s: Swap) {
        if let Some(sha) = &s.held {
            self.holding.remove(sha);
            self.collect();
        }
    }
}
