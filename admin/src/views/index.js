import { ChartBarIcon, ChatsCircleIcon, ClockCounterClockwiseIcon, CoinsIcon, CrownIcon, DevicesIcon, DiamondIcon, FlagIcon, GavelIcon, WarningOctagonIcon, GearIcon, GiftIcon, HeartbeatIcon, MegaphoneIcon, RobotIcon, ShieldCheckIcon, SquaresFourIcon, StarIcon, StickerIcon, UsersIcon } from '@phosphor-icons/react';
import Overview from './overview.jsx';
import Statistics from './stats.jsx';
import Members from './members.jsx';
import Sessions from './sessions.jsx';
import Conversations from './conversations.jsx';
import Bots from './bots.jsx';
import Moderation from './moderation.jsx';
import Appeals from './appeals.jsx';
import Reports from './reports.jsx';
import Broadcast from './broadcast.jsx';
import Packs from './packs.jsx';
import Premium from './premium.jsx';
import Stars from './stars.jsx';
import Gifts from './gifts.jsx';
import Fragment from './fragment.jsx';
import Circulation from './circulation.jsx';
import Health from './health.jsx';
import Audit from './audit.jsx';
import Countries from './countries.jsx';
import Settings from './settings.jsx';

export const views = {
    overview: { title: 'Overview', description: 'What is happening on your instance right now.', icon: SquaresFourIcon, component: Overview },
    stats: { title: 'Statistics', description: 'Messages, sign-ups and activity over time.', icon: ChartBarIcon, component: Statistics },
    members: { title: 'Members', description: 'Manage people on your instance.', icon: UsersIcon, component: Members },
    sessions: { title: 'Sessions', description: 'Every signed-in device across all members.', icon: DevicesIcon, component: Sessions },
    conversations: { title: 'Groups & channels', description: 'Manage the spaces where your community connects.', icon: ChatsCircleIcon, component: Conversations },
    bots: { title: 'Bots', description: 'Built-in services and member-created bots.', icon: RobotIcon, component: Bots },
    messages: { title: 'Moderation', description: 'Review conversations and keep your community healthy.', icon: ShieldCheckIcon, component: Moderation },
    reports: { title: 'Reports', description: 'What people reported, and what was done about it.', icon: WarningOctagonIcon, component: Reports },
    appeals: { title: 'Appeals', description: 'Frozen accounts asking to be let back.', icon: GavelIcon, component: Appeals },
    broadcast: { title: 'Broadcast', description: 'Send an announcement to your members.', icon: MegaphoneIcon, component: Broadcast },
    circulation: { title: 'Gifts & Collectibles', description: 'What is in circulation: gifts, collectibles, Fragment names and numbers, and who holds them.', icon: CoinsIcon, component: Circulation },
    packs: { title: 'Stickers & emoji', description: 'Manage pack availability and access.', icon: StickerIcon, component: Packs },
    premium: { title: 'Premium', description: 'Manage subscriptions and member benefits.', icon: CrownIcon, component: Premium },
    stars: { title: 'Stars', description: 'Create transactions and review the Stars history.', icon: StarIcon, component: Stars },
    gifts: { title: 'Gifts', description: 'What people can send each other for Stars.', icon: GiftIcon, component: Gifts },
    fragment: { title: 'Fragment', description: 'Reserve identifiers and manage administrator listings.', icon: DiamondIcon, component: Fragment },
    health: { title: 'Server health', description: 'Process, database and request metrics.', icon: HeartbeatIcon, component: Health },
    audit: { title: 'Audit log', description: 'Every administrative change, accounted for.', icon: ClockCounterClockwiseIcon, component: Audit },
    countries: { title: 'Countries & codes', description: 'Manage local login country names and dialing prefixes.', icon: FlagIcon, component: Countries },
    settings: { title: 'Settings', description: 'Configure how your community works.', icon: GearIcon, component: Settings }
};

export const groups = [
    { label: 'Community', views: ['overview', 'stats', 'members', 'sessions', 'conversations', 'bots'] },
    { label: 'Content', views: ['messages', 'reports', 'appeals', 'broadcast', 'packs', 'circulation'] },
    { label: 'Economy', views: ['premium', 'stars', 'gifts', 'fragment'] },
    { label: 'System', views: ['health', 'audit', 'countries', 'settings'] }
];
