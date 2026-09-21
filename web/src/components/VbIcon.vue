<script setup lang="ts">
// One icon from the sprite. `name` is the id without the `i-` prefix, so a
// caller writes <VbIcon name="net" /> and never a raw `#i-…` reference.
defineProps<{ name: string; size?: 'sm' | 'md' | 'lg' | 'xl' }>()
</script>

<template>
  <!-- viewBox is not optional: the sprite's paths are drawn in a 20×20 grid,
       and without it every icon renders at the browser's default 300×150 box
       and clips. -->
  <svg
    class="vb-ico"
    :class="`vb-ico--${size ?? 'md'}`"
    viewBox="0 0 20 20"
    aria-hidden="true"
    focusable="false"
  >
    <use :href="`#i-${name}`" />
  </svg>
</template>
