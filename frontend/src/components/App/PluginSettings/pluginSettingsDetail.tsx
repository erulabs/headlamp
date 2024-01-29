import Box from '@mui/material/Box';
import Button from '@mui/material/Button';
import Stack from '@mui/material/Stack';
import withStyles from '@mui/styles/withStyles';
import _ from 'lodash';
import { isValidElement, useEffect, useState } from 'react';
import { useDispatch } from 'react-redux';
import { useParams } from 'react-router-dom';
import { deletePlugin } from '../../../lib/k8s/apiProxy';
import { ConfigStore } from '../../../plugin/configStore';
import { PluginInfo, reloadPage } from '../../../plugin/pluginsSlice';
import { useTypedSelector } from '../../../redux/reducers/reducers';
import NotFoundComponent from '../../404';
import { SectionBox } from '../../common';
import { ConfirmDialog } from '../../common/Dialog';
import ErrorBoundary from '../../common/ErrorBoundary';

export default function PluginSettingsDetail() {
  const pluginSettings = useTypedSelector(state => state.plugins.pluginSettings);
  const { name } = useParams<{ name: string }>();
  const decodedName = decodeURIComponent(name);
  const plugin = pluginSettings.find(plugin => plugin.name === decodedName);
  if (!plugin) {
    return <NotFoundComponent />;
  }

  function handleDeleteConfirm() {
    if (!plugin) {
      return;
    }
    deletePlugin(plugin?.name)
      .then(() => {
        // update the plugin list
        const dispatch = useDispatch();
        dispatch(reloadPage());
      })
      .finally(() => {
        // redirect /plugins page
        // history.push isn't updating the plugins list so we're using window.location.pathname
        window.location.pathname = '/settings/plugins';
      });
  }

  const store = new ConfigStore(plugin.name);
  const pluginConf = store.useConfig();
  const config = pluginConf() as { [key: string]: any };

  function handleSave(data: { [key: string]: any }) {
    store.set(data);
  }

  return (
    <PluginSettingsDetailPure
      config={config}
      plugin={plugin}
      onSave={handleSave}
      onDelete={handleDeleteConfirm}
    />
  );
}

const ScrollableBox = withStyles(() => ({
  root: {
    overflowY: 'scroll',
    msOverflowStyle: 'none',
    scrollbarWidth: 'none',
    '&::-webkit-scrollbar': {
      display: 'none',
    },
  },
}))(Box);

export interface PluginSettingsDetailPureProps {
  config?: { [key: string]: any };
  plugin: PluginInfo;
  onSave?: (data: { [key: string]: any }) => void;
  onDelete: () => void;
}

export function PluginSettingsDetailPure(props: PluginSettingsDetailPureProps) {
  const { config, plugin, onSave, onDelete } = props;

  const [data, setData] = useState<{ [key: string]: any } | undefined>(config);
  const [enableSaveButton, setEnableSaveButton] = useState(false);
  const [openDeleteDialog, setOpenDeleteDialog] = useState(false);

  useEffect(() => {
    if (!_.isEqual(config, data)) {
      setEnableSaveButton(true);
    } else {
      setEnableSaveButton(false);
    }
  }, [data, config]);

  function onDataChange(data: { [key: string]: any }) {
    setData(data);
  }

  function handleSave() {
    if (onSave && data) {
      onSave(data);
    }
  }

  function handleDelete() {
    setOpenDeleteDialog(true);
  }

  function handleDeleteConfirm() {
    onDelete();
  }

  function handleCancel() {
    setData(config);
  }

  let component;
  if (isValidElement(plugin.settingsComponent)) {
    component = plugin.settingsComponent;
  } else if (typeof plugin.settingsComponent === 'function') {
    const Comp = plugin.settingsComponent;
    if (plugin.settingsAutoSave) {
      component = <Comp />;
    } else {
      component = <Comp onDataChange={onDataChange} data={data} />;
    }
  } else {
    component = null;
  }

  return (
    <>
      <SectionBox aria-live="polite" title={plugin.name} backLink={'/settings/plugins'}>
        {plugin.description}
        <ScrollableBox style={{ height: '70vh' }} py={0}>
          <ConfirmDialog
            open={openDeleteDialog}
            title={'Delete Plugin'}
            description={'Are you sure you want to delete this plugin?'}
            handleClose={() => setOpenDeleteDialog(false)}
            onConfirm={() => handleDeleteConfirm()}
          />
          <ErrorBoundary>{component}</ErrorBoundary>
        </ScrollableBox>
      </SectionBox>
      <Box py={0}>
        <Stack
          direction="row"
          spacing={2}
          justifyContent="space-between"
          alignItems="center"
          sx={{ borderTop: '2px solid', borderColor: 'silver', padding: '10px' }}
        >
          <Stack direction="row" spacing={1}>
            {!plugin.settingsAutoSave && (
              <>
                <Button
                  variant="contained"
                  disabled={!enableSaveButton}
                  style={{ backgroundColor: 'silver', color: 'black' }}
                  onClick={handleSave}
                >
                  Save
                </Button>
                <Button style={{ color: 'silver' }} onClick={handleCancel}>
                  Cancel
                </Button>
              </>
            )}
          </Stack>

          <Button variant="text" color="error" onClick={handleDelete}>
            Delete Plugin
          </Button>
        </Stack>
      </Box>
    </>
  );
}
