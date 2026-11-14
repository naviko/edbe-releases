// Links the engine: the static library for the target in lib/<os>_<arch>/ (put there by
// ./build.sh release), or the one in EDBE_LIB_DIR. Nothing is compiled.
use std::{env, path::PathBuf};

fn main() {
    println!("cargo:rerun-if-env-changed=EDBE_LIB_DIR");
    let os = env::var("CARGO_CFG_TARGET_OS").unwrap();
    let arch = match env::var("CARGO_CFG_TARGET_ARCH").unwrap().as_str() {
        "x86_64" => "amd64",
        "aarch64" => "arm64",
        other => other,
    }
    .to_string();
    let os = if os == "macos" { "darwin".to_string() } else { os };
    let dir = match env::var("EDBE_LIB_DIR") {
        Ok(d) => PathBuf::from(d),
        Err(_) => PathBuf::from(env::var("CARGO_MANIFEST_DIR").unwrap()).join("lib").join(format!("{os}_{arch}")),
    };
    if !dir.join("libedbe.a").exists() {
        panic!("edbe: this crate holds no engine for {os}_{arch} (looked for {})", dir.join("libedbe.a").display());
    }
    println!("cargo:rerun-if-changed={}", dir.join("libedbe.a").display());
    println!("cargo:rustc-link-search=native={}", dir.display());
    println!("cargo:rustc-link-lib=static=edbe");
}
