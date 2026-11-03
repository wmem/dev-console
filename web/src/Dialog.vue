<script setup lang="ts">
import {onMounted,ref} from 'vue';
defineProps<{title:string}>();const emit=defineEmits(['close']);const dialog=ref<HTMLDialogElement>();onMounted(()=>dialog.value?.showModal());
</script>
<template><dialog ref="dialog" @cancel.prevent="emit('close')" @click="if($event.target===dialog)emit('close');"><header class="dialog-head"><h2>{{title}}</h2><button class="icon-button" aria-label="关闭弹窗" @click="emit('close')">×</button></header><div class="dialog-body"><slot/></div></dialog></template>
