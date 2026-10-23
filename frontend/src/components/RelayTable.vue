<script setup>
defineProps({rows:{type:Array,default:()=>[]},columns:{type:Array,required:true},rowKey:{type:String,default:'id'},label:{type:String,default:'Records'}})
</script>
<template><div class="table-wrap"><table class="hub-table" :aria-label="label"><thead><tr><th v-for="column in columns" :key="column.key" scope="col">{{column.label}}</th></tr></thead><tbody><tr v-for="(row,index) in rows" :key="row[rowKey]??index"><td v-for="column in columns" :key="column.key"><slot :name="column.key" :row="row">{{row[column.key]??'—'}}</slot></td></tr></tbody></table></div></template>
