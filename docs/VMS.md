# Virtual machines, desktops and game streaming

## Virtual machines

A package of kind `vm` runs a whole operating system on one of your machines with [QEMU](https://www.qemu.org).

### What you need

QEMU on the machines that should run VMs. sys0 notices it on its own, and the marketplace enables VM packages once one of your machines has it.

| OS | Install | Hardware acceleration |
|---|---|---|
| Linux | `sudo apt install qemu-system-x86 qemu-utils` (or your distribution's equivalent; `qemu-system-arm` for ARM guests) | KVM |
| macOS | `brew install qemu` | Apple Hypervisor framework |
| Windows | the installer from [qemu.org](https://www.qemu.org/download/#windows) | Windows Hypervisor Platform (turn it on in "Windows features") |

When no accelerator is available, QEMU emulates the CPU in software: it works, but slowly. A VM of the machine's own architecture (x86_64 on Intel/AMD, aarch64 on Apple Silicon and ARM) gets acceleration, so the scheduler prefers those machines.

VMs run **only on your own machines**. Containers can be sandboxed on machines lent by others; a whole operating system can't be contained that way.

### How it works

1. **Image.** The package names a disk image by `https` URL and SHA-256. Each machine downloads it once, checks the checksum, and refuses it if it doesn't match.
2. **Disk.** The VM's disk is a copy-on-write layer over that image, so the image is never changed and many VMs can share it. Give the package a pooled volume and the layer lives in [pooled storage](STORAGE.md): **the VM moves to another machine with its disk**, and the layer is re-attached to that machine's copy of the image.
3. **First boot.** `cloudInit` user-data (users, passwords, SSH keys, packages) reaches the guest through cloud-init's NoCloud source, served by sys0 on the VM's private network. No ISO tools are needed. Inputs used in it are inserted as quoted strings, so a password can't inject configuration.
4. **Network.** The VM gets a private network with internet access, and declared `ports` are forwarded to it (`"2222:22"` makes the guest's SSH reachable on the machine's port 2222).
5. **Control.** sys0 drives QEMU's control socket (QMP): stopping the app presses the VM's power button and waits up to 90 seconds for a clean shutdown, and snapshots **pause the VM** while its disk is read, so the copy is consistent.

### Package fields

```json
"run": {
  "kind": "vm",
  "memoryMB": 2048,
  "network": true,
  "ports": ["2222:22"],
  "volumes": ["/disk"],
  "snapshotSeconds": 600,
  "vm": {
    "image": "https://cloud-images.ubuntu.com/releases/noble/release-20261017/ubuntu-24.04-server-cloudimg-amd64.img",
    "sha256": "04e8f2461a16a44e78a370284471f29616cdd9baf1893df25807b71f985b6c94",
    "arch": "x86_64",
    "cpus": 2,
    "diskGB": 20,
    "screen": true,
    "cloudInit": "#cloud-config\npassword: ${PASSWORD}\nchpasswd: {expire: false}\nssh_pwauth: true\n"
  }
}
```

| Field | Default | Meaning |
|---|---|---|
| `vm.image`, `vm.sha256` | required | Disk image (qcow2 or raw) and its checksum |
| `vm.arch` | `x86_64` | `x86_64` or `aarch64` |
| `vm.cpus` | 2 | Virtual CPUs |
| `vm.diskGB` | 20 | Disk size the guest sees (the layer only uses what's written) |
| `vm.screen` | off | A display you can open from the dashboard |
| `vm.cloudInit` | none | cloud-init user-data for the first boot |
| `memoryMB` | required | Memory, at least 128 |
| `volumes` | none | One pooled volume: the VM's disk moves with it |

Snapshots of a VM disk copy the whole changed layer, so give VMs a longer `snapshotSeconds` (the built-in Ubuntu VMs use 600).

### Built in

- **Ubuntu Server VM**: Ubuntu 24.04, x86_64. Set the `ubuntu` user's password when you deploy; SSH on port 2222.
- **Ubuntu Server VM (ARM)**: the same for Apple Silicon and other ARM machines.

## Screens: remote desktop for VMs

A VM with `"screen": true` gets a display. On the dashboard's **Apps** tab, **Open screen** opens it in a new browser tab:

- The page connects to the VM's machine with **noVNC** (MPL-2.0, bundled in the dashboard, no third-party servers).
- The display uses **VNC over WebSocket** on the machine, protected by a random **password per VM**, which the page fetches through your logged-in dashboard, so it never appears in a URL.
- **Ctrl+Alt+Del** and **Full screen** buttons are at the top.

Your browser has to be able to reach the VM's machine (same network or a VPN). The screen port is open on that machine's network, guarded by its password; VMs only run on your own machines.

## Linux desktop in your browser

The **Linux desktop in your browser** package runs [LinuxServer's Webtop](https://docs.linuxserver.io/images/docker-webtop/): Ubuntu with the XFCE desktop in a container, used from any browser on port 3000. You set the user name and password when you deploy. Its home folder is pooled, so it moves with the app. It runs only on your own machines and needs a container runtime.

## Game streaming (experimental)

The **Game streaming host** package turns one of your gaming PCs into a cloud gaming host with [Sunshine](https://app.lizardbyte.dev/Sunshine/): play its games on any phone, tablet, TV or laptop with the free [Moonlight](https://moonlight-stream.org) app.

- Install Sunshine on the gaming PC first. Streaming a real GPU needs Sunshine running natively, not in a container.
- The package runs only on your own machines, needs at least 2 GB of GPU memory, and goes to the machine with the most free GPU memory. If Sunshine isn't installed on a machine, sys0 says so and the pool tries another.
- Pair Moonlight with Sunshine as usual (Sunshine's own web page on the gaming PC).

It's marked experimental: CI has no GPU, so it hasn't been tested end to end the way VMs have.
