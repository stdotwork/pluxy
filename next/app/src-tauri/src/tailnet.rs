//! Whether Tailscale carries the connection from this computer to an address. Tailscale gives each
//! device an address in 100.64.0.0/10 or fd7a:115c:a1e0::/48 and encrypts what goes between them,
//! but 100.64.0.0/10 is also the shared address space ISPs use (RFC 6598), so an address of that
//! shape alone says nothing: the traffic goes over Tailscale only when this computer has a
//! Tailscale interface. That is read from the system's list of interfaces (`local_addresses`) each
//! time it is asked; when the list cannot be read, Tailscale is not taken to carry anything.
use std::net::{IpAddr, Ipv4Addr, Ipv6Addr};

/// Whether ip is in one of Tailscale's ranges: 100.64.0.0/10 or fd7a:115c:a1e0::/48.
pub fn in_range(ip: IpAddr) -> bool {
    match ip {
        IpAddr::V4(v4) => {
            let o = v4.octets();
            o[0] == 100 && o[1] & 0xc0 == 64
        }
        IpAddr::V6(v6) => v6.segments()[..3] == [0xfd7a, 0x115c, 0xa1e0],
    }
}

/// Whether Tailscale carries what this computer sends to ip, given locals, the addresses of this
/// computer's interfaces that are up: one of them is in Tailscale's ranges (a Tailscale interface),
/// and ip is in those ranges and is not one of this computer's own addresses.
pub fn carries_with(ip: IpAddr, locals: &[IpAddr]) -> bool {
    in_range(ip) && !locals.contains(&ip) && locals.iter().any(|l| in_range(*l))
}

/// The addresses of this computer's interfaces that are up.
#[cfg(unix)]
pub fn local_addresses() -> std::io::Result<Vec<IpAddr>> {
    let mut list: *mut libc::ifaddrs = std::ptr::null_mut();
    // SAFETY: getifaddrs fills list with a linked list that freeifaddrs frees; every node and its
    // address are read only before that.
    unsafe {
        if libc::getifaddrs(&mut list) != 0 {
            return Err(std::io::Error::last_os_error());
        }
        let mut out = Vec::new();
        let mut node = list;
        while !node.is_null() {
            let n = &*node;
            if !n.ifa_addr.is_null() && n.ifa_flags & libc::IFF_UP as libc::c_uint != 0 {
                match i32::from((*n.ifa_addr).sa_family) {
                    libc::AF_INET => {
                        let a = &*(n.ifa_addr as *const libc::sockaddr_in);
                        out.push(IpAddr::V4(Ipv4Addr::from(u32::from_be(a.sin_addr.s_addr))));
                    }
                    libc::AF_INET6 => {
                        let a = &*(n.ifa_addr as *const libc::sockaddr_in6);
                        out.push(IpAddr::V6(Ipv6Addr::from(a.sin6_addr.s6_addr)));
                    }
                    _ => {}
                }
            }
            node = n.ifa_next;
        }
        libc::freeifaddrs(list);
        Ok(out)
    }
}

/// The addresses of this computer's interfaces that are up.
#[cfg(windows)]
pub fn local_addresses() -> std::io::Result<Vec<IpAddr>> {
    use windows_sys::Win32::Foundation::{ERROR_BUFFER_OVERFLOW, NO_ERROR};
    use windows_sys::Win32::NetworkManagement::IpHelper::{
        GetAdaptersAddresses, GAA_FLAG_SKIP_ANYCAST, GAA_FLAG_SKIP_DNS_SERVER,
        GAA_FLAG_SKIP_MULTICAST, IP_ADAPTER_ADDRESSES_LH,
    };
    use windows_sys::Win32::NetworkManagement::Ndis::IfOperStatusUp;
    use windows_sys::Win32::Networking::WinSock::{
        AF_INET, AF_INET6, AF_UNSPEC, SOCKADDR_IN, SOCKADDR_IN6,
    };
    let flags = GAA_FLAG_SKIP_ANYCAST | GAA_FLAG_SKIP_MULTICAST | GAA_FLAG_SKIP_DNS_SERVER;
    // u64 words keep the buffer aligned for the structures written into it.
    let mut size: u32 = 16 * 1024;
    let mut buf: Vec<u64>;
    loop {
        buf = vec![0u64; (size as usize).div_ceil(8)];
        // SAFETY: buf holds size bytes; the call writes at most that many and says how many it
        // needs when they do not fit.
        let r = unsafe {
            GetAdaptersAddresses(
                u32::from(AF_UNSPEC),
                flags,
                std::ptr::null(),
                buf.as_mut_ptr() as *mut IP_ADAPTER_ADDRESSES_LH,
                &mut size,
            )
        };
        if r == NO_ERROR {
            break;
        }
        if r != ERROR_BUFFER_OVERFLOW {
            return Err(std::io::Error::from_raw_os_error(r as i32));
        }
    }
    let mut out = Vec::new();
    // SAFETY: on success buf holds a linked list of adapters and their unicast addresses, read
    // only while buf lives.
    unsafe {
        let mut adapter = buf.as_ptr() as *const IP_ADAPTER_ADDRESSES_LH;
        while !adapter.is_null() {
            let a = &*adapter;
            if a.OperStatus == IfOperStatusUp {
                let mut unicast = a.FirstUnicastAddress;
                while !unicast.is_null() {
                    let u = &*unicast;
                    let sa = u.Address.lpSockaddr;
                    if !sa.is_null() {
                        match (*sa).sa_family {
                            AF_INET => {
                                let s = &*(sa as *const SOCKADDR_IN);
                                out.push(IpAddr::V4(Ipv4Addr::from(u32::from_be(
                                    s.sin_addr.S_un.S_addr,
                                ))));
                            }
                            AF_INET6 => {
                                let s = &*(sa as *const SOCKADDR_IN6);
                                out.push(IpAddr::V6(Ipv6Addr::from(s.sin6_addr.u.Byte)));
                            }
                            _ => {}
                        }
                    }
                    unicast = u.Next;
                }
            }
            adapter = a.Next;
        }
    }
    Ok(out)
}

#[cfg(not(any(unix, windows)))]
pub fn local_addresses() -> std::io::Result<Vec<IpAddr>> {
    Err(std::io::Error::other("no interface list on this system"))
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A Tailscale-shaped address goes over Tailscale only when this computer has a Tailscale
    /// interface, and never when it is this computer's own Tailscale address; an address outside
    /// Tailscale's ranges never does.
    #[test]
    fn tailscale_carries_only_with_a_tailscale_interface_here() {
        let ip = |s: &str| s.parse::<IpAddr>().unwrap();
        let lan = [ip("127.0.0.1"), ip("192.168.1.5"), ip("::1")];
        let tailnet = [
            ip("127.0.0.1"),
            ip("192.168.1.5"),
            ip("100.101.1.2"),
            ip("fd7a:115c:a1e0::2"),
        ];
        for target in ["100.92.228.112", "100.64.0.1", "fd7a:115c:a1e0::1"] {
            assert!(
                !carries_with(ip(target), &lan),
                "{target}: no Tailscale interface here, so it is ISP shared space or unreachable"
            );
            assert!(carries_with(ip(target), &tailnet), "{target}");
        }
        assert!(!carries_with(ip("100.101.1.2"), &tailnet), "this computer");
        for elsewhere in [
            "10.0.0.2",
            "192.168.1.20",
            "100.63.255.255",
            "100.128.0.1",
            "fd7a:115c:a1e1::1",
            "::ffff:100.64.0.1",
        ] {
            assert!(!carries_with(ip(elsewhere), &tailnet), "{elsewhere}");
        }
    }

    /// The system's list holds at least the loopback interface, which is up.
    #[test]
    fn the_interface_list_is_read() {
        let locals = local_addresses().unwrap();
        assert!(locals.iter().any(IpAddr::is_loopback), "{locals:?}");
    }
}
