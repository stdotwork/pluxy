use super::*;

const PASSWD: &str = "\
root:x:0:0:root:/root:/bin/bash
daemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin
nobody:x:65534:65534:nobody:/nonexistent:/usr/sbin/nologin
systemd-network:x:998:998:systemd Network Management:/:/usr/sbin/nologin
alice:x:1000:1000::/home/alice:/bin/bash
bob:x:1001:1001::/home/bob:/bin/bash
svc:x:1002:1002::/home/svc:/usr/sbin/nologin
locked:x:1003:1003::/home/locked:/bin/false
nohome:x:1004:1004::relative:/bin/sh
Upper:x:1005:1005::/home/Upper:/bin/sh
broken line
carol:x:notanumber:1:::/bin/sh
";

fn names(v: &Value) -> Vec<String> {
    v["users"]
        .as_array()
        .unwrap()
        .iter()
        .map(|u| u["name"].as_str().unwrap().to_string())
        .collect()
}

#[test]
fn users_are_people_with_a_login_shell_and_root_last() {
    let v = users(&format!("alice\nx86_64\n{PASSWD}")).unwrap();
    assert_eq!(names(&v), ["alice", "bob", "root"]);
    assert_eq!(v["default"], "alice");
    assert_eq!(v["arch"], "amd64");
    let u = &v["users"];
    assert_eq!(
        u[0],
        json!({"name": "alice", "uid": 1000, "home": "/home/alice", "default": true, "root": false})
    );
    assert_eq!(u[1]["default"], false);
    assert_eq!(
        u[2],
        json!({"name": "root", "uid": 0, "home": "/root", "default": false, "root": true})
    );
}

#[test]
fn the_default_user_is_who_the_distribution_starts_as() {
    // Right after `wsl --install --no-launch` the default user is root.
    let v = users(&format!("root\naarch64\n{PASSWD}")).unwrap();
    assert_eq!(v["default"], "root");
    assert_eq!(v["arch"], "arm64");
    assert_eq!(v["users"][2]["default"], true);
}

#[test]
fn a_processor_pluxy_has_no_build_for_is_refused() {
    let e = users(&format!("alice\nriscv64\n{PASSWD}")).unwrap_err();
    assert_eq!(e.code, "unsupported");
    assert!(e.message.contains("riscv64"), "{e}");
    assert_eq!(users("alice\n").unwrap_err().code, "unavailable");
}

const MOUNTINFO: &str = "\
1 0 8:32 / / rw,relatime - ext4 /dev/sdc rw,discard
2 1 0:20 / /usr/lib/wsl/drivers ro,nosuid,nodev,noatime - 9p drivers ro,dirsync,aname=drivers
3 1 0:21 / /mnt/wsl rw,relatime shared:1 - tmpfs none rw
4 1 0:52 / /mnt/c rw,noatime - 9p C:\\134 rw,dirsync,aname=drvfs;path=C:\\;uid=1000;gid=1000
5 1 0:53 / /mnt/d rw,noexec,noatime - 9p D:\\134 rw,aname=drvfs;path=D:\\
6 1 0:54 / /home/bob/my\\040docs rw - ext4 /dev/sdd rw
";

#[test]
fn drives_are_wsls_mounts_of_one_letter_folders() {
    let (d, root) = drives(MOUNTINFO);
    assert_eq!(
        d,
        json!([
            {"letter": "c", "path": "/mnt/c", "exec": true},
            {"letter": "d", "path": "/mnt/d", "exec": false}
        ])
    );
    assert_eq!(root, "/mnt/");
    // WSL 1 mounts drives as drvfs; a custom automount root.
    let wsl1 = "10 1 0:44 / /win/c rw,noatime - drvfs C:\\134 rw,case=off\n";
    let (d, root) = drives(wsl1);
    assert_eq!(d, json!([{"letter": "c", "path": "/win/c", "exec": true}]));
    assert_eq!(root, "/win/");
    // No drive mounted (automount off).
    let (d, root) = drives("1 0 8:32 / / rw - ext4 /dev/sdc rw\n");
    assert_eq!(d, json!([]));
    assert!(root.is_null());
}

#[test]
fn mount_points_are_unescaped() {
    let m = mounts(MOUNTINFO);
    assert_eq!(m[5].point, "/home/bob/my docs");
    assert_eq!(m[3].fstype, "9p");
    assert_eq!(m[4].options, "rw,noexec,noatime");
}

#[test]
fn check_reads_the_users_home_and_refuses_another_user_or_distribution() {
    let out = format!("bob\n1001\n/home/bob\nx86_64\nUbuntu\n{MOUNTINFO}");
    let v = check(&out, "Ubuntu", "bob").unwrap();
    assert_eq!(v["home"], "/home/bob");
    assert_eq!(v["uid"], 1001);
    assert_eq!(v["arch"], "amd64");
    assert_eq!(v["automount"], "/mnt/");
    assert_eq!(v["drives"][0]["path"], "/mnt/c");
    let e = check(&out, "Ubuntu", "alice").unwrap_err();
    assert!(
        e.message
            .contains("expected WSL Ubuntu/alice, this is Ubuntu/bob"),
        "{e}"
    );
    let e = check(&out, "Other", "bob").unwrap_err();
    assert!(
        e.message
            .contains("expected WSL Other/bob, this is Ubuntu/bob"),
        "{e}"
    );
    let e = check("bob\n1001\n\nx86_64\nUbuntu\n", "Ubuntu", "bob").unwrap_err();
    assert!(e.message.contains("no home folder"), "{e}");
}

#[test]
fn failures_say_whether_the_distribution_or_the_user_is_missing() {
    let e = failure(
        -1,
        "There is no distribution with the supplied name. Error code: Wsl/Service/WSL_E_DISTRO_NOT_FOUND",
        "Nope",
        Some("bob"),
    );
    assert_eq!(e.code, "not_found");
    assert!(e.message.contains("no WSL distribution named Nope"), "{e}");
    let e = failure(-1, "User not found.", "Ubuntu", Some("zed"));
    assert_eq!(e.code, "not_found");
    assert!(e.message.contains("user zed was not found"), "{e}");
    // WSL 1 gives a connection error for a missing user.
    let e = failure(-1, "Error: 0xd000020c", "Ubuntu", Some("zed"));
    assert_eq!(e.code, "not_found");
    assert!(e.message.contains("or it could not be started"), "{e}");
    let e = failure(127, "sh: getent: not found", "Ubuntu", None);
    assert_eq!(e.code, "unavailable");
}

#[test]
fn of_mounts_stacked_on_one_folder_the_last_counts() {
    // A tmpfs mounted over the drive at /mnt/c: it is no drive now. A noexec mount over /mnt/d.
    let stacked = "\
1 0 8:32 / / rw - ext4 /dev/sdc rw
2 1 0:52 / /mnt/c rw - 9p C:\\134 rw,aname=drvfs
3 1 0:53 / /mnt/d rw - 9p D:\\134 rw,aname=drvfs
4 2 0:54 / /mnt/c rw - tmpfs none rw
5 3 0:55 / /mnt/d rw,noexec - 9p D:\\134 rw,aname=drvfs
";
    let (d, root) = drives(stacked);
    assert_eq!(d, json!([{"letter": "d", "path": "/mnt/d", "exec": false}]));
    assert_eq!(root, "/mnt/");
}

/// The host makes its record of an environment from `check`'s answer (`Record::from_check` in
/// `next/app/src-tauri`, whose test reads this same file). This is that answer for the output
/// below, with the `wsl` version `check` adds on Windows, so a change to either side's fields
/// breaks one of the two tests.
#[test]
fn check_answers_what_the_hosts_record_reads() {
    let out = format!("bob\n1001\n/home/bob\nx86_64\nUbuntu\n{MOUNTINFO}");
    let mut v = check(&out, "Ubuntu", "bob").unwrap();
    v["wsl"] = serde_json::json!(2);
    let want: Value = serde_json::from_str(include_str!("../testdata/check.json")).unwrap();
    assert_eq!(v, want, "{}", serde_json::to_string_pretty(&v).unwrap());
}
