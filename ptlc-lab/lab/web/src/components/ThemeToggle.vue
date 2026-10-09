<script setup lang="ts">
import Icon from './Icon.vue'
import { theme, toggleTheme } from '../theme.ts'
</script>

<template>
  <button type="button" class="icon-btn" :title="`Switch to the ${theme === 'dark' ? 'light' : 'dark'} theme`" @click="toggleTheme">
    <Icon :name="theme === 'dark' ? 'sun' : 'moon'" :size="15" />
    <span class="sr-only">Switch theme</span>
  </button>
</template>
