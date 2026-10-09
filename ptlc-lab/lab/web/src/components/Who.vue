<script setup lang="ts">
// An address, with the name the lab knows it by when it has one.
import { computed } from 'vue'
import HexValue from './HexValue.vue'
import { nameOf } from '../store.ts'

const props = defineProps<{ address: string; full?: boolean }>()
const name = computed(() => nameOf(props.address))
</script>

<template>
  <span class="who">
    <strong v-if="name">{{ name }}</strong>
    <HexValue :value="address" :head="8" :tail="6" :full="full" />
  </span>
</template>

<style scoped>
.who {
  display: inline-flex;
  align-items: baseline;
  gap: 7px;
  flex-wrap: wrap;
  max-width: 100%;
}
.who strong {
  font-weight: 600;
}
</style>
