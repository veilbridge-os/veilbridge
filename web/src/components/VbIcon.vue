<script setup lang="ts">
// One icon, resolved by name (M2.9).
//
// The accepted artboards draw every icon they can from Element Plus's own set
// — 28 of 37 — and keep nine of their own for shapes it has no counterpart
// for. The code follows the same split: the registry below is the single place
// that says which glyph a name means, so a screen writes <VbIcon name="net" />
// and never picks a glyph for itself. Two screens choosing separately is how a
// product ends up teaching two vocabularies for one thing.
import {
  Aim,
  ArrowRight,
  Bottom,
  CircleCheck,
  Clock,
  Close,
  Cpu,
  Delete,
  Edit,
  EditPen,
  Expand,
  Files,
  Filter,
  Grid,
  Guide,
  Loading,
  Location,
  Lock,
  Monitor,
  Odometer,
  Operation,
  Paperclip,
  Plus,
  Rank,
  RefreshLeft,
  Search,
  Setting,
  Share,
  Sort,
  SwitchButton,
  Top,
  Warning,
} from '@element-plus/icons-vue'
import type { Component } from 'vue'
import { computed } from 'vue'
import {
  VbBridge,
  VbCable,
  VbGlobe,
  VbIPv6,
  VbPlug,
  VbSnow,
  VbUsb,
  VbWiFi,
  VbWiFiOff,
} from './icons/custom'

const props = defineProps<{ name: string; size?: 'sm' | 'md' | 'lg' | 'xl' }>()

// Same mapping as Icons-Compare.dc.html in the mockup project.
const REGISTRY: Record<string, Component> = {
  dash: Odometer,
  net: Share,
  policy: Guide,
  rule: Operation,
  sys: Setting,
  dev: Monitor,
  mem: Cpu,
  disk: Files,
  search: Search,
  chev: ArrowRight,
  alert: Warning,
  out: SwitchButton,
  check: CircleCheck,
  target: Aim,
  clock: Clock,
  undo: RefreshLeft,
  loop: Loading,
  draft: EditPen,
  plus: Plus,
  close: Close,
  trash: Delete,
  lock: Lock,
  menu: Expand,
  node: Location,
  fork: Sort,
  pin: Paperclip,
  tabs: Grid,
  // The firewall screen (#36): the section, a drag handle, the two arrows of a
  // move, and "change" — the pen is already "draft".
  filter: Filter,
  grip: Rank,
  up: Top,
  down: Bottom,
  edit: Edit,
  // Ours: Element Plus has no counterpart.
  globe: VbGlobe,
  wifi: VbWiFi,
  'wifi-off': VbWiFiOff,
  cable: VbCable,
  plug: VbPlug,
  usb: VbUsb,
  snow: VbSnow,
  v6: VbIPv6,
  bridge: VbBridge,
}

// An unknown name is a mistake in the caller, not something to paper over with
// a random glyph: nothing is drawn, and the gap is visible in review.
const glyph = computed<Component | null>(() => REGISTRY[props.name] ?? null)
</script>

<template>
  <el-icon v-if="glyph" class="vb-ico" :class="`vb-ico--${size ?? 'md'}`">
    <component :is="glyph" />
  </el-icon>
</template>
