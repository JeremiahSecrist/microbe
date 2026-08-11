{ config, lib, pkgs, modulesPath, ... }:

{
  imports = [
    # Shared SSH/admin configuration, identical to the live ISO target.
    ./base-config.nix
  ];

  networking.hostName = "microvm";

  # NixOS's default availableKernelModules includes physical-hardware drivers
  # (atkbd, ahci, etc.) that don't exist in the trimmed microVM kernel.
  # Restrict to what's actually present and relevant.
  boot.initrd.availableKernelModules = lib.mkForce [ "erofs" ];


  microvm = {
    hypervisor = "cloud-hypervisor";
    vcpu = 2;
    mem = 1024;
    vsock.cid = 3;
    interfaces = [
      {
        type = "tap";
        # Host-side tap name, created declaratively by the `lappy` host config
        # (systemd-networkd netdev with MultiQueue=true, owner "sky").
        id = "unc0";
        mac = "02:00:00:00:00:02";
      }
    ];
  };

  # Static LAN between host tap (192.168.99.1) and guest (192.168.99.2).
  # microvm.optimize enables systemd-networkd, so configure networking with
  # networkd units. The guest NIC is matched by MAC (02:00:00:00:00:02)
  # instead of by name: `unc0` is only the host-side tap name, and the
  # virtio NIC inside the VM gets an unpredictable name (e.g. enp0s1).
  networking.useDHCP = false;
  networking.nameservers = [ "1.1.1.1" ];
  systemd.network.enable = lib.mkDefault true;
  systemd.network.networks.microvm = {
    matchConfig.MACAddress = "02:00:00:00:00:02";
    linkConfig.RequiredForOnline = "no";
    networkConfig.DNS = [ "1.1.1.1" ];
    address = [
      "192.168.99.2/24"
    ];
    routes = [
      { Gateway = "192.168.99.1"; }
    ];
  };
}
