# Getting back into a router after a network change

Every network change the panel applies waits for you to confirm that the panel
still answers. If you do not confirm, the router puts the previous settings
back by itself. So in most cases the way back is simply: **do nothing and wait**
— the panel tells you the time, usually under two minutes.

This page is for the case that is left: a change that **was confirmed** (from a
path that still worked, say a cable in a local port) and then turned out to cut
the way you use to reach the router.

## What the command does

```sh
veilbridged -restore-network
```

- Puts back the **network, firewall and address-handout settings** from before
  the last change that stuck. Changes the router undid by itself are skipped —
  their settings are the same as now.
- Touches nothing else: Wi-Fi, the system settings and passwords stay as they
  are.
- Before it writes anything, it keeps the current settings as a restore point,
  and prints the command that undoes the restore.
- If a change is still waiting for confirmation, it does nothing and tells you
  when the router will undo that change by itself (exit code 2). Add `-force`
  to restore anyway.

To see what can be restored, and to go further back than the last change:

```sh
veilbridged -list-restore-points
veilbridged -restore-point uci-20260926T163000Z
```

The router keeps the last five restore points; the panel takes one before every
change it applies.

## Way 1: ssh over a cable

1. Plug a computer into one of the router's **local** (LAN) ports.
2. Open the panel at the router's local address (by default
   `http://192.168.1.1:8080`). If it answers, you can fix the setting there.
3. If it does not: `ssh root@192.168.1.1` (use the router's local address if
   you changed it) and run `veilbridged -restore-network`.

## Way 2: a console

A router with a serial console, or a virtual machine with a console on its
hypervisor, gives you a shell with no network at all. Log in and run
`veilbridged -restore-network`.

## Way 3: OpenWrt failsafe mode

When there is no network way in at all — the local address itself is broken —
OpenWrt's failsafe mode starts the router with default network settings and a
root shell, ignoring the saved configuration.

1. Enter failsafe mode. On most routers: power it on and, when the status light
   starts blinking fast, press any button (often reset or WPS) once. The exact
   way depends on the model — see the
   [OpenWrt failsafe guide](https://openwrt.org/docs/guide-user/troubleshooting/failsafe_and_factory_reset).
   On a console, press `f` and Enter when asked during boot.
2. Give your computer the address `192.168.1.2`, mask `255.255.255.0`, and
   plug it into a LAN port. Then `ssh root@192.168.1.1` (no password in
   failsafe mode).
3. Make the saved configuration and VeilBridge available, restore, reboot:

   ```sh
   mount_root
   veilbridged -restore-network
   reboot -f
   ```

If the command says the services could not reload, that is expected in failsafe
mode — the files are already back, and the reboot applies them.

## Last resort

A factory reset (`firstboot` in failsafe mode, or holding the reset button for
longer than five seconds on most routers) erases **all** settings, VeilBridge's
included. Use it only if none of the above works.

## Verified on

- A Cudy WR3000S v1 (OpenWrt 25.12.5): a firewall rule blocking the panel from
  the internet side, confirmed from inside and still in place after the
  confirmation window; `veilbridged -restore-network` over ssh put the firewall
  back byte for byte and the panel answered again.
- An x86-64 OpenWrt 23.05.5 virtual machine: an uplink change that cut the
  machine off completely (panel and ssh), confirmed from inside — restored from
  the hypervisor console; and again the same way from **failsafe mode**
  (`mount_root`, restore, reboot), after which the machine came up with its
  settings back.
- The same router, with a change still waiting for confirmation: the command
  refused and named the time the router would undo it — which it then did.
