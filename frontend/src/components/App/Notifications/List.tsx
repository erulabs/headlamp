import { Icon } from '@iconify/react';
import { Box, IconButton, Menu, MenuItem, Tooltip, Typography, useTheme } from '@mui/material';
import useMediaQuery from '@mui/material/useMediaQuery';
import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useDispatch } from 'react-redux';
import { useHistory } from 'react-router-dom';
import { useTypedSelector } from '../../../redux/reducers/reducers';
import { DateLabel, SectionBox, SectionFilterHeader, SimpleTable } from '../../common';
import Empty from '../../common/EmptyContent';
import { Notification, setNotifications, updateNotifications } from './notificationsSlice';

export default function NotificationList() {
  const notifications = useTypedSelector(state => state.notifications.notifications);
  const clusters = useTypedSelector(state => state.config.clusters);
  const { t } = useTranslation(['glossary', 'translation']);
  const dispatch = useDispatch();
  const theme = useTheme();
  const search = useTypedSelector(state => state.filter.search);
  const history = useHistory();

  const allNotificationsAreDeleted = useMemo(() => {
    return !notifications.find(notification => !notification.deleted);
  }, [notifications]);

  const hasUnseenNotifications = useMemo(() => {
    return !!notifications.find(notification => !notification.deleted && !notification.seen);
  }, [notifications]);

  function notificationSeenUnseenHandler(event: any, notification?: Notification) {
    if (!notification) {
      return;
    }
    dispatch(updateNotifications(notification));
  }

  function clearAllNotifications() {
    const massagedNotifications = notifications.map(notification => {
      const updatedNotification = Object.assign(new Notification(), notification);
      updatedNotification.deleted = true;
      return updatedNotification;
    });
    dispatch(setNotifications(massagedNotifications));
  }

  function markAllAsRead() {
    const massagedNotifications = notifications.map(notification => {
      const updatedNotification = Object.assign(new Notification(), notification);
      updatedNotification.seen = true;
      return updatedNotification;
    });
    dispatch(setNotifications(massagedNotifications));
  }

  function notificationItemClickHandler(notification: Notification) {
    notification.url && history.push(notification.url);
    notification.seen = true;
    dispatch(updateNotifications(notification));
  }

  function NotificationActionMenu() {
    const [anchorEl, setAnchorEl] = useState(null);

    function handleClick(event: any) {
      setAnchorEl(event.currentTarget);
    }

    function handleClose() {
      setAnchorEl(null);
    }

    return (
      <>
        <IconButton size="medium">
          <Icon icon="mdi:dots-vertical" onClick={handleClick} />
        </IconButton>
        <Menu anchorEl={anchorEl} open={Boolean(anchorEl)} onClose={handleClose}>
          <MenuItem onClick={markAllAsRead} disabled={!hasUnseenNotifications}>
            <Typography color={'primary'}>{t('translation|Mark all as read')}</Typography>
          </MenuItem>
          <MenuItem onClick={clearAllNotifications} disabled={allNotificationsAreDeleted}>
            <Typography color="primary">{t('translation|Clear all')}</Typography>
          </MenuItem>
        </Menu>
      </>
    );
  }

  // Placeholder for custom styles
  // We can use this along with the cellProps property in the columns array to style individual cells
  const isSmallScreen = useMediaQuery('(max-width:900px)');
  const isMediumScreen = useMediaQuery('(max-width:1199px)');
  const isLargeScreen = useMediaQuery('(min-width:1200px)');

  const [currentMessageWidth, setCurrentMessageWidth] = useState('auto');

  useEffect(() => {
    console.log('smallScreen', isSmallScreen);
    console.log('mediumScreen', isMediumScreen);
    console.log('largeScreen', isLargeScreen);
    setCurrentMessageWidth(getMessageWidth());
    console.log('currentMessageWidth', currentMessageWidth);
  }, [isSmallScreen, isMediumScreen, isLargeScreen]);

  /** While these may seem oddly specific, they are just the right size to prevent link and calendar icon clipping */
  const getMessageWidth = () => {
    if (isSmallScreen) {
      return '30vw';
    } else if (isMediumScreen) {
      return '40vw';
    } else if (isLargeScreen) {
      return '50vw';
    } else {
      return 'auto';
    }
  };

  /** Custom style to apply to different columns */
  const customStyles = {
    notifMessage: {
      minWidth: currentMessageWidth,
    },
    notifLink: {
      backgroundColor: 'cyan',
      maxWidth: 'auto',
      display: 'flex',
      alignItems: 'center',
      justifyContent: 'center',
    },
    notifTimestamp: {
      maxWidth: 'auto',
    },
    notifVisible: {
      maxWidth: 'auto',
    },
  };

  return (
    <SectionBox
      title={
        <SectionFilterHeader
          title={t('translation|Notifications')}
          noNamespaceFilter
          actions={[<NotificationActionMenu />]}
        />
      }
      backLink
    >
      {allNotificationsAreDeleted ? (
        <Empty> {t("translation|You don't have any notifications right now")}</Empty>
      ) : (
        <Box
          style={{
            maxWidth: '100%',
          }}
        >
          <SimpleTable
            filterFunction={(notification: Notification) =>
              (notification?.message?.toLowerCase() || '').includes(search.toLowerCase())
            }
            columns={[
              {
                label: t('translation|Message'),
                cellProps: {
                  style: customStyles.notifMessage,
                },
                getter: (notification: Notification) => (
                  <Box>
                    <Tooltip
                      title={notification.message || t('translation|No message')}
                      disableHoverListener={!notification.message}
                    >
                      <Typography
                        style={{
                          fontWeight: notification.seen ? 'normal' : 'bold',
                          cursor: 'pointer',
                        }}
                        noWrap
                        onClick={() => notificationItemClickHandler(notification)}
                      >
                        {`${notification.message || t(`translation|No message`)}`}
                      </Typography>
                    </Tooltip>
                  </Box>
                ),
              },
              {
                label: t('glossary|Cluster'),
                cellProps: {
                  style: customStyles.notifLink,
                },
                getter: (notification: Notification) => (
                  <Box display={'flex'} alignItems="center">
                    {Object.entries(clusters || {}).length > 1 && notification.cluster && (
                      <Box
                        border={1}
                        p={0.5}
                        mr={1}
                        textOverflow="ellipsis"
                        overflow={'hidden'}
                        whiteSpace="nowrap"
                      >
                        {notification.cluster}
                      </Box>
                    )}{' '}
                  </Box>
                ),
              },
              {
                label: t('translation|Date'),
                cellProps: {
                  style: customStyles.notifTimestamp,
                },
                getter: (notification: Notification) => <DateLabel date={notification.date} />,
              },
              {
                label: t('translation|Visible'),
                cellProps: {
                  style: customStyles.notifVisible,
                },
                getter: (notification: Notification) =>
                  !notification.seen && (
                    <Tooltip title={t(`translation|Mark as read`)}>
                      <IconButton
                        onClick={e => notificationSeenUnseenHandler(e, notification)}
                        aria-label={t(`translation|Mark as read`)}
                        size="medium"
                      >
                        <Icon
                          icon="mdi:circle"
                          color={theme.palette.error.main}
                          height={12}
                          width={12}
                        />
                      </IconButton>
                    </Tooltip>
                  ),
              },
            ]}
            data={notifications}
            noTableHeader
          />
        </Box>
      )}
    </SectionBox>
  );
}
