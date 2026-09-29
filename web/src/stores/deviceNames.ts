// What the panel calls each device, by hardware address, for places that
// only get the address: the apply bar reads a device's internet being turned
// off as the address the router's rule holds (#53), and "Internet: on → off"
// under 9a:ef:51:8b:96:d6 is the operating system talking. The devices screen
// fills this whenever it reads the list; elsewhere the address is the
// fallback, never a guess.
import { reactive } from 'vue'

export const deviceNames = reactive(new Map<string, string>())

export function rememberNames(devices: { mac: string; name?: string; reportedName?: string }[]) {
  for (const d of devices) {
    const name = d.name || d.reportedName
    if (name) deviceNames.set(d.mac, name)
  }
}
