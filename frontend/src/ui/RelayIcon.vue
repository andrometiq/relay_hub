<script setup>
import { computed } from 'vue'
import {
  AlertCircle, ArrowDown, ArrowLeft, ArrowUpRight, Bell, Bot, Briefcase, Check, ChevronDown, ChevronRight,
  Circle, Clock, Copy, CornerUpLeft, Crosshair, Download, ExternalLink, Eye, File, FileText,
  Filter, Forward, GitBranch, Globe, Grid, Image, Inbox, Info, Languages, Link, List, LoaderCircle,
  Key, LogOut, MapPin, Menu, MessageCircle, MessageSquare, Mic, MicOff, Monitor, Moon, MoreHorizontal,
  MoreVertical, Music, PanelRight, Paperclip, Phone, PhoneOff, Pin, Play, Plus, Radio, RefreshCw,
  Repeat, Reply, Scan, Search, Send, Settings, Smile, Sparkles, Star, Sun, Trash, Upload, User,
  UserCheck, Users, Video, WandSparkles, X, Zap,
} from '@lucide/vue'

const props = defineProps({ name: { type: String, default: 'circle' }, size: { type: [String, Number], default: 16 } })
const icons = { AlertCircle, ArrowDown, ArrowLeft, ArrowUpRight, Bell, Bot, Briefcase, Check, ChevronDown, ChevronRight, Circle, Clock, Copy, CornerUpLeft, Crosshair, Download, ExternalLink, Eye, File, FileText, Filter, Forward, GitBranch, Globe, Grid, Image, Inbox, Info, Key, Languages, Link, List, LoaderCircle, LogOut, MapPin, Menu, MessageCircle, MessageSquare, Mic, MicOff, Monitor, Moon, MoreHorizontal, MoreVertical, Music, PanelRight, Paperclip, Phone, PhoneOff, Pin, Play, Plus, Radio, RefreshCw, Repeat, Reply, Scan, Search, Send, Settings, Smile, Sparkles, Star, Sun, Trash, Upload, User, UserCheck, Users, Video, WandSparkles, X, Zap }
const aliases = { actions: 'WandSparkles', scope: 'Crosshair', viewport: 'Scan', sidebar: 'PanelRight', menu: 'Menu', 'log-out': 'LogOut' }
const component = computed(() => {
  const pascal = props.name.split('-').map(part => part.charAt(0).toUpperCase() + part.slice(1)).join('')
  return icons[aliases[props.name] || pascal] || icons.Circle
})
</script>

<template><component :is="component" :size="size" :stroke-width="1.8" aria-hidden="true" /></template>
