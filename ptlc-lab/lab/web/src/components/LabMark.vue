<script setup lang="ts">
// The lab's mark: a P drawn by the rules of the Rune Z, as Glyphswap's G and
// Karum's K are. The same 24-unit grid, one 2.6 stroke with mitred joins, the
// corners of the bowl cut at 45 degrees, and the plasma gradient pinned to
// the grid in user space so the family shares one vertical light.
//
// public/favicon.svg is the same path; change them together.
withDefaults(defineProps<{ size?: number }>(), { size: 26 })
</script>

<template>
  <svg :width="size" :height="size" viewBox="0 0 24 24" fill="none" aria-hidden="true">
    <defs>
      <linearGradient id="ptlc-rune-plasma" gradientUnits="userSpaceOnUse" x1="0" y1="4" x2="0" y2="20">
        <stop offset="0%" stop-color="hsl(120 86% 63%)" />
        <stop offset="100%" stop-color="hsl(145 100% 38%)" />
      </linearGradient>
    </defs>
    <path d="M6.6,19.6V5.3H14.6L18.7,9.4V11.4L14.6,15.5H10.6" stroke="url(#ptlc-rune-plasma)" stroke-width="2.6" stroke-linecap="square" stroke-linejoin="miter" />
  </svg>
</template>
